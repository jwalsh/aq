# aq chaos test suite

Standalone chaos tests for the aq gossip layer. Validates **build step 7** acceptance criteria from CLAUDE.md:

> 10 agents, 100 msg/min, p99 < 500ms

## How to run

Build the aq binary first, then run the chaos tests:

```bash
go build -o aq .
go run contrib/chaos/chaos.go --aq-binary ./aq
```

Or use the Makefile target:

```bash
make demo-chaos
```

## Flags

| Flag           | Default | Description                                      |
|----------------|---------|--------------------------------------------------|
| `--aq-binary`  | `./aq`  | Path to the aq binary                            |
| `--duration`   | `30s`   | Duration for sustained load scenario             |
| `--agents`     | `10`    | Number of simulated agents for sustained load    |
| `--scenario`   | `all`   | Which scenario to run (see below)                |

## Scenarios

| # | Name       | What it tests                                        | PASS criteria                              |
|---|------------|------------------------------------------------------|--------------------------------------------|
| 1 | sustained  | Sustained load (build step 7 proof)                  | p99 latency < 500ms                        |
| 2 | burst      | 500 announces fired as fast as possible              | >= 80% of broadcasts visible               |
| 3 | conflict   | Conflict detection with 10 agents on same file       | Mechanism runs without error                |
| 4 | ttl        | Broadcasts with 3s TTL appear and expire on schedule | Appear within 2s, disappear within 5s      |
| 5 | archive    | 200 broadcasts with TTL=1, verify cleanup            | 0 active broadcasts after 3s               |
| 6 | fanout     | Scaling at N=2, 5, 10, 20, 50 agents                | p99 < 500ms through N=10 (WARN above that) |

Run a single scenario:

```bash
go run contrib/chaos/chaos.go --aq-binary ./aq --scenario sustained
go run contrib/chaos/chaos.go --aq-binary ./aq --scenario burst
go run contrib/chaos/chaos.go --aq-binary ./aq --scenario ttl
```

## What PASS/FAIL means

- **PASS**: The acceptance criteria for that scenario are met. The aq gossip layer performs within specified bounds.
- **WARN**: Performance exceeds thresholds at high fan-out (N>10), which is expected behavior given filesystem-first transport.
- **FAIL**: The acceptance criteria are not met. This is a signal that either the implementation has a performance regression or the test environment is constrained.

Exit code 0 if all scenarios pass, 1 if any fail.

## Expected run time

- Single scenario: 10-30 seconds
- All scenarios: approximately 2 minutes

## Architecture

The chaos test is a `//go:build ignore` Go program that shells out to the aq binary via `os/exec`. It does not import from the aq package. Each scenario creates an isolated temporary `AQ_HOME` directory, which is cleaned up after the scenario completes.

This design means:
- The chaos test does not affect the main build
- It tests the binary as a user would invoke it
- Scenarios are isolated from each other and from the user's `~/.aq`
- Only stdlib is required
