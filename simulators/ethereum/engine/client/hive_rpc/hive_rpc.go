package hive_rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	api "github.com/ethereum/go-ethereum/core/beacon"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/hive/hivesim"
	"github.com/ethereum/hive/simulators/ethereum/engine/client"
	"github.com/ethereum/hive/simulators/ethereum/engine/globals"
	"github.com/ethereum/hive/simulators/ethereum/engine/helper"
	"github.com/golang-jwt/jwt/v4"
)

type HiveRPCEngineStarter struct {
	// Client parameters used to launch the default client
	ClientType              string
	ChainFile               string
	TerminalTotalDifficulty *big.Int
	EnginePort              int
	EthPort                 int
	JWTSecret               []byte
}

func (s HiveRPCEngineStarter) StartClient(T *hivesim.T, testContext context.Context, ClientParams hivesim.Params, ClientFiles hivesim.Params, bootClients ...client.EngineClient) (client.EngineClient, error) {
	var (
		clientType = s.ClientType
		enginePort = s.EnginePort
		ethPort    = s.EthPort
		jwtSecret  = s.JWTSecret
		ttd        = s.TerminalTotalDifficulty
	)
	if clientType == "" {
		cs, err := T.Sim.ClientTypes()
		if err != nil {
			return nil, fmt.Errorf("Client type was not supplied and simulator returned error on trying to get all client types: %v", err)
		}
		if cs == nil || len(cs) == 0 {
			return nil, fmt.Errorf("Client type was not supplied and simulator returned empty client types: %v", cs)
		}
		clientType = cs[0].Name
	}
	if enginePort == 0 {
		enginePort = globals.EnginePortHTTP
	}
	if ethPort == 0 {
		ethPort = globals.EthPortHTTP
	}
	if jwtSecret == nil {
		jwtSecret = globals.DefaultJwtTokenSecretBytes
	}
	if s.ChainFile != "" {
		ClientFiles = ClientFiles.Set("/chain.rlp", "./chains/"+s.ChainFile)
	}
	if _, ok := ClientFiles["/genesis.json"]; !ok {
		return nil, fmt.Errorf("Cannot start without genesis file")
	}
	if ttd == nil {
		if ttdStr, ok := ClientParams["HIVE_TERMINAL_TOTAL_DIFFICULTY"]; ok {
			// Retrieve TTD from parameters
			ttd, ok = new(big.Int).SetString(ttdStr, 10)
			if !ok {
				return nil, fmt.Errorf("Unable to parse TTD from parameters")
			}
		}
	} else {
		ttdInt := helper.CalculateRealTTD(ClientFiles["/genesis.json"], ttd.Int64())
		ClientParams = ClientParams.Set("HIVE_TERMINAL_TOTAL_DIFFICULTY", fmt.Sprintf("%d", ttdInt))
	}
	if bootClients != nil && len(bootClients) > 0 {
		var (
			enodes = make([]string, len(bootClients))
			err    error
		)
		for i, bootClient := range bootClients {
			enodes[i], err = bootClient.EnodeURL()
			if err != nil {
				return nil, fmt.Errorf("Unable to obtain bootnode: %v", err)
			}
		}
		enodeString := strings.Join(enodes, ",")
		ClientParams = ClientParams.Set("HIVE_BOOTNODE", enodeString)
	}

	// Start the client and create the engine client object
	c := T.StartClient(clientType, ClientParams, hivesim.WithStaticFiles(ClientFiles))
	if err := CheckEthEngineLive(c); err != nil {
		return nil, fmt.Errorf("Engine/Eth ports were never open for client: %v", err)
	}
	ec := NewHiveRPCEngineClient(c, enginePort, ethPort, jwtSecret, ttd, &helper.LoggingRoundTrip{
		T:     T,
		Hc:    c,
		Inner: http.DefaultTransport,
	})
	return ec, nil
}

func CheckEthEngineLive(c *hivesim.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	var (
		ticker = time.NewTicker(100 * time.Millisecond)
		dialer net.Dialer
	)
	defer ticker.Stop()
	for _, checkport := range []int{globals.EthPortHTTP, globals.EnginePortHTTP} {
		addr := fmt.Sprintf("%s:%d", c.IP, checkport)
	portcheckloop:
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				conn, err := dialer.DialContext(ctx, "tcp", addr)
				if err == nil {
					conn.Close()
					break portcheckloop
				}
			}
		}
	}
	return nil
}

type AccountTransactionInfo struct {
	PreviousBlock common.Hash
	PreviousNonce uint64
}

// Implements the EngineClient interface for a normal RPC client.
type HiveRPCEngineClient struct {
	*ethclient.Client
	h              *hivesim.Client
	c              *rpc.Client
	cEth           *rpc.Client
	ttd            *big.Int
	JWTSecretBytes []byte

	// Engine updates info
	latestFcUStateSent *api.ForkchoiceStateV1
	latestPAttrSent    *api.PayloadAttributesV1
	latestFcUResponse  *api.ForkChoiceResponse

	latestPayloadSent          *api.ExecutableDataV1
	latestPayloadStatusReponse *api.PayloadStatusV1

	// Test account nonces
	accTxInfoMap map[common.Address]*AccountTransactionInfo
}

// NewClient creates a engine client that uses the given RPC client.
func NewHiveRPCEngineClient(h *hivesim.Client, enginePort int, ethPort int, jwtSecretBytes []byte, ttd *big.Int, transport http.RoundTripper) *HiveRPCEngineClient {
	client := &http.Client{
		Transport: transport,
	}
	// Prepare HTTP Client
	rpcHttpClient, _ := rpc.DialHTTPWithClient(fmt.Sprintf("http://%s:%d/", h.IP, enginePort), client)

	// Prepare ETH Client
	client = &http.Client{
		Transport: transport,
	}
	rpcClient, _ := rpc.DialHTTPWithClient(fmt.Sprintf("http://%s:%d/", h.IP, ethPort), client)
	eth := ethclient.NewClient(rpcClient)
	return &HiveRPCEngineClient{
		h:              h,
		c:              rpcHttpClient,
		Client:         eth,
		cEth:           rpcClient,
		ttd:            ttd,
		JWTSecretBytes: jwtSecretBytes,
		accTxInfoMap:   make(map[common.Address]*AccountTransactionInfo),
	}
}

func (ec *HiveRPCEngineClient) ID() string {
	return ec.h.Container
}

func (ec *HiveRPCEngineClient) EnodeURL() (string, error) {
	return ec.h.EnodeURL()
}

func (ec *HiveRPCEngineClient) TerminalTotalDifficulty() *big.Int {
	return ec.ttd
}

var (
	Head      *big.Int // Nil
	Pending   = big.NewInt(-2)
	Finalized = big.NewInt(-3)
	Safe      = big.NewInt(-4)
)

// Custom toBlockNumArg to test `safe` and `finalized`
func toBlockNumArg(number *big.Int) string {
	if number == nil {
		return "latest"
	}
	if number.Cmp(Pending) == 0 {
		return "pending"
	}
	if number.Cmp(Finalized) == 0 {
		return "finalized"
	}
	if number.Cmp(Safe) == 0 {
		return "safe"
	}
	return hexutil.EncodeBig(number)
}

func (ec *HiveRPCEngineClient) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	var header *types.Header
	err := ec.cEth.CallContext(ctx, &header, "eth_getBlockByNumber", toBlockNumArg(number), false)
	if err == nil && header == nil {
		err = ethereum.NotFound
	}
	return header, err
}

// Helper structs to fetch the TotalDifficulty
type TD struct {
	TotalDifficulty *hexutil.Big `json:"totalDifficulty"`
}
type TotalDifficultyHeader struct {
	types.Header
	TD
}

func (tdh *TotalDifficultyHeader) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &tdh.Header); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &tdh.TD); err != nil {
		return err
	}
	return nil
}

func (ec *HiveRPCEngineClient) GetTotalDifficulty(ctx context.Context) (*big.Int, error) {
	var td *TotalDifficultyHeader
	if err := ec.cEth.CallContext(ctx, &td, "eth_getBlockByNumber", "latest", false); err == nil {
		return td.TotalDifficulty.ToInt(), nil
	} else {
		return nil, err
	}
}

func (ec *HiveRPCEngineClient) Close() error {
	ec.c.Close()
	ec.Client.Close()
	return nil
}

// JWT Tokens
func GetNewToken(jwtSecretBytes []byte, iat time.Time) (string, error) {
	newToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iat": iat.Unix(),
	})
	tokenString, err := newToken.SignedString(jwtSecretBytes)
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

func (ec *HiveRPCEngineClient) PrepareAuthCallToken(jwtSecretBytes []byte, iat time.Time) error {
	newTokenString, err := GetNewToken(jwtSecretBytes, iat)
	if err != nil {
		return err
	}
	ec.c.SetHeader("Authorization", fmt.Sprintf("Bearer %s", newTokenString))
	return nil
}

func (ec *HiveRPCEngineClient) PrepareDefaultAuthCallToken() error {
	ec.PrepareAuthCallToken(ec.JWTSecretBytes, time.Now())
	return nil
}

// Engine API Call Methods
func (ec *HiveRPCEngineClient) ForkchoiceUpdatedV1(ctx context.Context, fcState *api.ForkchoiceStateV1, pAttributes *api.PayloadAttributesV1) (api.ForkChoiceResponse, error) {
	var result api.ForkChoiceResponse
	if err := ec.PrepareDefaultAuthCallToken(); err != nil {
		return result, err
	}
	ec.latestFcUStateSent = fcState
	ec.latestPAttrSent = pAttributes
	err := ec.c.CallContext(ctx, &result, "engine_forkchoiceUpdatedV1", fcState, pAttributes)
	ec.latestFcUResponse = &result
	return result, err
}

func (ec *HiveRPCEngineClient) GetPayloadV1(ctx context.Context, payloadId *api.PayloadID) (api.ExecutableDataV1, error) {
	var result api.ExecutableDataV1
	if err := ec.PrepareDefaultAuthCallToken(); err != nil {
		return result, err
	}
	err := ec.c.CallContext(ctx, &result, "engine_getPayloadV1", payloadId)
	return result, err
}

func (ec *HiveRPCEngineClient) NewPayloadV1(ctx context.Context, payload *api.ExecutableDataV1) (api.PayloadStatusV1, error) {
	var result api.PayloadStatusV1
	if err := ec.PrepareDefaultAuthCallToken(); err != nil {
		return result, err
	}
	ec.latestPayloadSent = payload
	err := ec.c.CallContext(ctx, &result, "engine_newPayloadV1", payload)
	ec.latestPayloadStatusReponse = &result
	return result, err
}

func (ec *HiveRPCEngineClient) ExchangeTransitionConfigurationV1(ctx context.Context, tConf *api.TransitionConfigurationV1) (api.TransitionConfigurationV1, error) {
	var result api.TransitionConfigurationV1
	err := ec.c.CallContext(ctx, &result, "engine_exchangeTransitionConfigurationV1", tConf)
	return result, err
}

func (ec *HiveRPCEngineClient) GetNextAccountNonce(testCtx context.Context, account common.Address) (uint64, error) {
	// First get the current head of the client where we will send the tx
	ctx, cancel := context.WithTimeout(testCtx, globals.RPCTimeout)
	defer cancel()
	head, err := ec.HeaderByNumber(ctx, nil)
	if err != nil {
		return 0, err
	}
	// Then check if we have any info about this account, and when it was last updated
	if accTxInfo, ok := ec.accTxInfoMap[account]; ok && accTxInfo != nil && (accTxInfo.PreviousBlock == head.Hash() || accTxInfo.PreviousBlock == head.ParentHash) {
		// We have info about this account and is up to date (or up to date until the very last block).
		// Increase the nonce and return it
		accTxInfo.PreviousBlock = head.Hash()
		accTxInfo.PreviousNonce++
		return accTxInfo.PreviousNonce, nil
	}
	// We don't have info about this account, or is outdated, or we re-org'd, we must request the nonce
	ctx, cancel = context.WithTimeout(testCtx, globals.RPCTimeout)
	defer cancel()
	nonce, err := ec.NonceAt(ctx, account, head.Number)
	if err != nil {
		return 0, err
	}
	ec.accTxInfoMap[account] = &AccountTransactionInfo{
		PreviousBlock: head.Hash(),
		PreviousNonce: nonce,
	}
	return nonce, nil
}

func (ec *HiveRPCEngineClient) PostRunVerifications() error {
	// There are no post run verifications for RPC clients yet
	return nil
}

func (ec *HiveRPCEngineClient) LatestForkchoiceSent() (fcState *api.ForkchoiceStateV1, pAttributes *api.PayloadAttributesV1) {
	return ec.latestFcUStateSent, ec.latestPAttrSent
}

func (ec *HiveRPCEngineClient) LatestNewPayloadSent() *api.ExecutableDataV1 {
	return ec.latestPayloadSent
}

func (ec *HiveRPCEngineClient) LatestForkchoiceResponse() *api.ForkChoiceResponse {
	return ec.latestFcUResponse
}
func (ec *HiveRPCEngineClient) LatestNewPayloadResponse() *api.PayloadStatusV1 {
	return ec.latestPayloadStatusReponse
}
