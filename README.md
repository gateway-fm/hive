# hive - Ethereum end-to-end test harness

Ethereum grew large to the point where testing implementations is a huge burden. Unit tests are fine
for checking various implementation quirks, but validating that a client conforms to some baseline
quality or validating that clients can play nicely together in a multi client environment is all but
simple.

This project is meant to serve as an easily expandable test harness where **anyone** can add tests in
**any** programming language felt comfortable with and it should simultaneously be able to run those
tests against all potential clients. As such the harness is meant to do black box testing where no
client specific internal details/state can be tested and/or inspected, rather emphasis being put on
adherence to official specs or behaviours under different circumstances.

Most importantly, it is essential to be able to run this test suite as part of the CI workflow! To
this effect the entire suite is based on docker containers.

# Installing the hive validator

The `hive` project is based on Go. Although there are plans to make running `hive` possible using
only docker, for now you'll need a valid Go (1.6 and upwards) installation available.

You can install `hive` via:

```
$ go get github.com/karalabe/hive
```

*Note: For now `hive` requires running from the repository root as it needs access to quite a number
of resource files to build the corrent docker images and containers. This requirement will be removed
in the future.*

# Validating clients

You can run the full suite of `hive` validation tests against all the known implementations tagged
`master` by simply running `hive` from the repository root. It will build a docker image for every
known client as well as one for every known validation test. **As this make take a while, you can
track the detailed progress in the `log.txt` file.**

The end result should be a JSON report, detailing for each client the list of validations failed and
those passed. If you wish to explore the reasons of failure, full logs from all clients and testers
are pushed into the `log.txt` log file.

```
$ hive --validate=go-ethereum:develop --validators=.
...
Validation results:
{
  "go-ethereum:develop": {
    "fail": [
      "ethereum/rpc-tests"
    ],
    "pass": [
      "smoke/genesis-chain-blocks",
      "smoke/genesis-only",
      "smoke/genesis-chain"
    ]
  }
}
```

You may request a different set of clients to be validated via the `--validate` regexp flag (e.g.
validating all `go-ethereum` versions would be `--validate go-ethereum:`). Similarly you may request
only a subset of validation tests to be run via the `--validators` regexp flag (e.g. running only the
smoke tests would be `--validators smoke/.`).

# Simulating clients

`hive` supports a more advanced form of client testing called *simulations*, where entire networka
of clients are run concurrently under various circumstances and teir behavior monitored and checked.

Running network simulations is completely analogous to validations from the user's perspective: you
can specify which clients to simulate with the `--simulate` regexp flag, and you can specify which
simulations to run via the `--simulators` regexp flag. By default simulations aren't being run as
they can be quite lengthy.

Similarly to validations, end result of simulations should be a JSON report, detailing for each
client the list of simulations failed and those passed. Likewise, if you wish to explore the reasons
of failure, full logs from all clients and testers are pushed into the `log.txt` log file.

```
$ hive --validate="" --simulate=go-ethereum:develop --simulators=.
...
Simulation results:
{
  "go-ethereum:develop": {
    "pass": [
      "dao-hard-fork",
      "smoke/single-node"
    ]
  }
}
```

Currently `hive` does not support simulating mixed networks (i.e. different Ethereum implementations).
This will be expanded in the future when we learn a bit more about the tests people write and how
those can be usefully checked against multiple client types.

# Adding new clients

The `hive` test harness can validate arbitrary implementations of the [Ethereum yellow paper](http://gavwood.com/paper.pdf).

Being based on docker containers, `hive` is pretty liberal on pretty much all aspects of a client
implementation:

 * `hive` doesn't care what dependencies a client has: language, libraries or otherwise.
 * `hive` doesn't care how the client is built: environment, tooling or otherwise.
 * `hive` doesn't care what garbage clients generates during execution.

As long as a client can run on Linux, and you can package it up into a Docker image, `hive` can test it!

## Creating a client image

Adding a new client implementation to `hive` entails creating a Dockerfile (and related resources),
based on which `hive` will assemble the docker image to use as the blueprint for testing.

The client definition(s) should reside in the `clients` folder, each named `<project>:<tag>` where
`<project>` is the official name of the client (lowercase, no fancy characters), and `<tag>` is an
arbitrary id up to the client maintainers to make the best use of. `hive` will automatically pick
up all clients from this folder.

There are little contraints on the image itself, though a few required caveats are:

 * It should be as tiny as possible (play nice with others). Preferrably use `alpine` Linux.
 * It should expose the following ports: 8545 (HTTP RPC), 8546 (WS RPC), 30303 (devp2p).
 * It should have a single entrypoint (script?) defined, which can initialize and run the client.

For guidance, check out the reference [`go-ethereum:master`](https://github.com/karalabe/hive/tree/master/clients/go-ethereum:master/Dockerfile) client.

### Initializing the client

Since `hive` does not want to enforce any CLI parametrization scheme on client implementations, it
injects all the required configurations into the Linux containers prior to launching the client's
`entrypoint` script. It is then left to this script to interpret all the environmental configs and
initialize the client appropriately.

The chain configurations files:

 * `/genesis.json` contains the JSON specification of the Ethereum genesis states
 * `/chain.rlp` contains a batch of RLP encoded blocks to import before startup
 * `/blocks/` folder with numbered singleton blocks to import before startup

Client startup scripts need to ensure that they load the genesis state first, then import a possibly
longer blockchain and then import possibly numerous individual blocks. The reason for requiring two
different block sources is that specifying a singe chain is more optimal, but tests requiring forking
chains cannot create a single chain.

Beside the standardized chain configurations, clients can in general be modified behavior wise in
quite a few ways that are mostly supported by all clients, yet are implemented differently in each.
As such, each possible behavioral change required by some validator or simulator is characterized by
an environment variable, which clients should interpret as best as they can.

The behavioral configuration variables:

  * `HIVE_BOOTNODE` enode URL of the discovery-only node to bootstrap the client
  * `HIVE_TESTNET` whether clients should run with modified starting nonces (`2^20`)
  * `HIVE_NODETYPE` specifying the sync and pruning algos that should be used
    * If unset, then uninteresting and run in the node's default mode
    * If `archive`, assumes that all historical state is retained after sync
    * If `full`, assumes fast sync and consecutive pruning of historical state
    * If `light`, assumes header only sync and no state maintenance at all
  * `HIVE_FORK_HOMESTEAD` the block number of the Ethereum Homestead transition
  * `HIVE_FORK_DAO` the block number of the DAO hard-fork transition (unset = no fork)

### Starting the client

After initializing the client blockchain (genesis, chain, blocks), the last task of the entry script
is to start up the client itself. The following defaults are required by `hive` to enable automatic
network assembly and firewall enforcement:

 * Clients should open their HTTP-RPC endpoint on `0.0.0.0:8545` (mandatory)
 * Clients should open their WS-RPC endpoint on `0.0.0.0:8546` (optional)
 * Clients should open their IPC-RPC endpoints at `/rpc.ipc` (optional)

There is no need to handle graceful client termination. Clients will be forcefully aborted upon test
suite completion and all related data purged. A new instance will be started for every test.

### Smoke testing new clients

To quickly check if a client adheres to the requirements of `hive`, there is a suite of smoke test
validations and simulations that just initialize clients with some pre-configured states and query
it from the various RPC endpoints.

```
$ hive --smoke go-ethereum:master
...
Validation results:
{
  "go-ethereum:master": {
    "pass": [
      "smoke/genesis-chain",
      "smoke/genesis-chain-blocks",
      "smoke/genesis-only"
    ]
  }
}
...
Simulation results:
{
  "go-ethereum:master": {
    "pass": [
      "smoke/single-node"
    ]
  }
}
```

*Note: All smoke tests must pass for a client to be included into `hive`.*

## Under the hood

# Adding new validators

## Under the hood

# Adding new simulators

## Under the hood

# Contributions

This project takes a different approach to code contributions than your usual FOSS project with well
ingrained maintainers and relatively few external contributors. It is an experiment, whether it will
work out or not is for the future to decide.

We follow the [Collective Code Construction Contract (C4)](http://rfc.zeromq.org/spec:22/C4/), code
contribution model, as expanded and explained in [The ZeroMQ Process](https://hintjens.gitbooks.io/social-architecture/content/chapter4.html).
The core idea being that any patch that successfully solves an issue (bug/feature) and doesn't break
any existing code/contracts **must** be optimistically merged by maintainers. Followup patches may
be used to for additional polishes – and patches may even be outright reverted if they turn out to
have a negative impact – but no change must be rejected based on personal values.

Please consult the two C4 documents for details:

 * [Collective Code Construction Contract (C4)](http://rfc.zeromq.org/spec:22/C4/)
 * [The ZeroMQ Process](https://hintjens.gitbooks.io/social-architecture/content/chapter4.html)

# License

The `hive` project is licensed under the [GNU General Public License v3.0](http://www.gnu.org/licenses/gpl-3.0.en.html),
also included in our repository in the COPYING file.