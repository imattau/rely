# Quantum-Inspired Nostr Relay

A production-ready Nostr relay built on [rely v2](../../README.md) that uses **continuous-time quantum walk propagation** to decide which notes to proactively fetch from peer relays, combined with a **gossip-based reputation and consensus layer** to suppress spam at the network level.

---

## Features

| Feature | Description |
|---|---|
| **Quantum walk propagation** | Propagation probability from relay *s* to relay *i* is computed as \|⟨i\|exp(−iLt)\|s⟩\|², where *L* is the graph Laplacian of the relay mesh |
| **Reputation damping** | Negative-reputation pubkeys have their propagation amplitude damped by `exp(−2γ\|rep\|t)`, making spam harder to propagate network-wide |
| **Diffusive consensus** | Peers converge on a shared round counter and reputation map through neighbour-averaging gossip with clamped `[−1, 1]` scores |
| **P2P peer mesh** | Typed WebSocket envelope protocol (`type`/`payload`) with 30-second keepalive pings and buffered per-peer send queues |
| **Token-bucket rate limiting** | Independent per-client and per-peer token buckets; no external dependencies |
| **In-memory storage** | Thread-safe event store with filter matching and per-pubkey reputation |
| **YAML configuration** | Zero-dependency config parser; sane defaults if file is missing |
| **NIP-01 / NIP-11 / NIP-42** | Full Nostr protocol support via rely v2 |

---

## How It Works

### 1. Relay graph & Laplacian

At startup, all known relay URLs (local + configured peers) are assembled into a graph. An adjacency list is maintained in `GraphState`. When topology changes, the graph Laplacian is recomputed:

```
L[i][i] = degree(i)
L[i][j] = -1  if connected, else 0
```

For graphs up to 128 nodes, exact eigendecomposition is performed using the built-in **Jacobi algorithm** (no external linear-algebra library required). For larger graphs, propagation falls back to a **truncated Taylor expansion** of exp(−iLt) applied as a sparse matrix-vector product (up to 16 terms, converging to 1×10⁻¹⁰).

### 2. Quantum walk propagation

When a note arrives (from a client event or a peer `note_announce`), it is registered with the `Propagator` along with its source relay and the current consensus round.

On each `quantum_tick`, the propagator calls `Tick(currentRound, γ)` which evaluates every active note:

```
t    = currentRound − bornRound
amp  = ⟨localIndex | exp(−iLt) | sourceIndex⟩
prob = |amp|²  × ReputationFactor(rep, γ, t)
```

If `prob > fetch_threshold`, the note is fetched from its source relay. A small **exploration floor** (`0.02 × (1 − exp(−t/25))`) prevents notes from getting permanently stuck at zero probability in weakly-connected graphs.

### 3. Reputation damping

```go
func ReputationFactor(rep, gamma, t float64) float64 {
    if rep >= 0 { return 1.0 }
    return math.Exp(-2.0 * gamma * math.Abs(rep) * t)
}
```

Positive reputation has no effect. Negative reputation exponentially suppresses propagation over time; the stronger the negative score and the higher γ, the faster the damping.

### 4. Diffusive consensus

The `Diffuser` runs a background ticker that:
1. Increments the local round counter
2. Broadcasts a `consensus` envelope to all peers containing only the **delta** (changed reputation scores since last broadcast)

On receiving a neighbour's state, round and reputation are merged by neighbour-averaging:

```
round = round(local + neighbour) / 2
rep[k] = clamp((local[k] + neighbour[k]) / 2, -1, 1)
```

This is a standard gossip diffusion protocol — values converge globally over O(diameter) rounds.

### 5. Spam protection

Two layers:
- **Token-bucket rate limiting** — per client-UID and per peer URL, configured events/sec
- **Reputation gate** — events from pubkeys with reputation < −0.5 are rejected at the rely `Reject.Event` hook before any processing

---

## Benchmark Results

Run on Intel Core i3-10110U @ 2.10GHz, Go 1.24, linux/amd64:

| Benchmark | Iterations | Time/op | Allocs/op |
|---|---|---|---|
| `PropagatorTick` — 100 notes, 10-relay graph | 817,332 | **3,954 ns** | 2 |
| `PropagatorTick` — 1,000 notes, 10-relay graph | 98,125 | **43,741 ns** | 2 |
| `PropagatorTick` — 5,000 notes, 10-relay graph | 19,513 | **203,242 ns** | 2 |
| `PropagatorTick` — 1,000 notes, 256-relay sparse | 7,472 | **410,229 ns** | 20 |
| `PropagatorTick` — shared source (1,000 notes) | 94,003 | **43,561 ns** | 2 |
| `PropagatorTick` — mixed sources (1,000 notes) | 52,398 | **74,082 ns** | 2 |
| `GraphRecompute` — 16-relay dense | 266,432 | **11,692 ns** | 35 allocs |
| `GraphRecompute` — 64-relay dense | 10,000 | **351,091 ns** | 131 allocs |
| `GraphRecompute` — 256-relay sparse (Taylor) | 100,000,000 | **30 ns** | 0 |
| `ScheduleRecompute` debounce (64-relay) | 48,400,712 | **70 ns** | 0 |
| `QuantumChurn` — topology change + tick (32 relays) | 49,532 | **85,945 ns** | 69 allocs |

Key observations:
- Tick is **allocation-near-free** (2 allocs regardless of note count) thanks to per-tick cache reuse keyed on `(sourceIndex, bornRound)` — notes sharing a source/round share a single amplitude computation.
- Sparse graphs >128 nodes skip eigendecomposition entirely and cost **30 ns/recompute** at zero allocations.
- A 1,000-note tick completes in **~44 µs** — well within a 1-second quantum tick budget.

---

## Configuration

```yaml
relay:
  listen: ":8080"
  name: "My Quantum Relay"
  description: "Nostr relay with quantum walk propagation"

quantum:
  gamma: 0.5                 # reputation damping strength
  fetch_threshold: 0.05      # minimum walk probability to trigger a fetch
  consensus_tick_ms: 500     # how often to gossip consensus state to peers
  quantum_tick_ms: 1000      # how often to evaluate propagation probabilities

spam:
  client_events_per_sec: 10  # token-bucket rate per client
  peer_announce_per_sec: 100 # token-bucket rate per peer relay

peers:
  - "ws://relay2.example.com"
  - "ws://relay3.example.com"
```

Place the file at `configs/config.yaml` relative to the binary. If the file is missing, all defaults above apply.

### Key parameters

| Parameter | Effect |
|---|---|
| `gamma` | Higher = faster damping of negative-reputation pubkeys. 0 disables damping. |
| `fetch_threshold` | Lower = fetch more aggressively. 0 fetches everything. |
| `consensus_tick_ms` | Faster = quicker reputation convergence; more network traffic. |
| `quantum_tick_ms` | Faster = notes fetched sooner; higher CPU. |

---

## Running

```bash
go build ./cmd/quantum-relay
./quantum-relay
```

Or with a config file:

```bash
./quantum-relay   # reads configs/config.yaml automatically
```

---

## Architecture

```
cmd/quantum-relay/
  main.go          — wiring: relay hooks, peer message dispatch, tick loops
  config.go        — YAML config loader (no external deps)

internal/
  quantum/
    graph.go       — GraphState: Laplacian, Jacobi eigensolver, sparse Taylor fallback
    walk.go        — Propagator: per-note amplitude evaluation, fetch triggering, eviction
    damping.go     — ReputationFactor: exp damping for negative-rep pubkeys
  consensus/
    diffuser.go    — Diffuser: round counter, delta-gossip, neighbour-averaging merge
  p2p/
    peer.go        — PeerManager: WebSocket dial, typed Envelope, buffered send/ping
  spam/
    detector.go    — RateLimiter: per-ID token buckets (no stdlib rate pkg needed)
  storage/
    store.go       — Store: in-memory events + per-pubkey reputation
```

---

## Package Boundaries

Each internal package has one responsibility and no inward dependencies on other internal packages. `main.go` owns all the wiring between them, keeping the packages independently testable.

---

## Tests

```bash
# Unit + integration
go test ./...

# Benchmarks
go test ./internal/quantum/ -bench=. -benchmem

# Swarm integration test (spins up multiple in-process relays)
go test ./tests/ -v -run TestQuantumSwarm

# Spam stress test
go test ./tests/ -v -run TestSpamStress
```
