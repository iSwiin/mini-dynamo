# Mini Dynamo (Go) — Eventually Consistent KV Store

A Dynamo-inspired distributed key-value store built in Go.
Supports quorum reads/writes, sloppy quorum + hinted handoff, tombstones, anti-entropy, and WAL/snapshot durability.

## Why this project
I built this Mini Dynamo project to demonstrate distributed systems fundamentals in production style code.
It implements a Dynamo inspired KV store in Go with configurable N/R/W quorums, sloppy quorum + durable hinted handoff, tombstones, read repair, anti entropy convergence, and WAL/snapshot persistence plus repeatable failure demos you can run locally (kill a node, keep writes working, restart, and verify convergence).


## Features
- **Consistent hashing ring + vnodes** for balanced distribution
- **Quorum reads/writes (N/R/W)** configurable in `nodes.json`
- **Sloppy quorum**: accept writes even if a preferred replica is down (by writing to a fallback)
- **Hinted handoff (durable)**: fallback stores a hint and later delivers to intended replica after recovery
- **LWW conflict resolution**: Last-Write-Wins (timestamp + writer tie-break)
- **Tombstones**: deletes replicate safely (no resurrection after failures)
- **Read-repair**: GET repairs stale replicas opportunistically
- **Anti-entropy**: background metadata sync converges “cold keys” that are never read
- **Durability**: per-node **KV WAL replay** on restart + optional **snapshots**
- Debug endpoints for visibility: `/debug/hints`, `/debug/ae`, `/debug/persist`

## Architecture diagram (simple)

~~~mermaid
flowchart LR
  Client --> Coordinator
  Coordinator --> Ring

  Coordinator --> Replica1
  Coordinator --> Replica2

  Coordinator --> Fallback
  Fallback --> HintWAL
  HintWAL --> Replica1

  Coordinator --> MemStore
  MemStore --> KVWAL
  MemStore --> Snapshot

  AntiEntropy --> Replica2
  AntiEntropy --> Coordinator
~~~



## How it works (high level)
- A node receiving a client request acts as **Coordinator**.
- Coordinator uses the **ring** to choose the **preferred replica set** (size **N**).
- Writes succeed after **W acknowledgements**; reads return after **R responses**.
- If a preferred replica is down, Coordinator writes to a **fallback** node (**sloppy quorum**) and includes a **hint** pointing to the intended target.
- Fallback persists the hint; a background loop later delivers the record to the intended replica (**hinted handoff**).
- **Anti-entropy** periodically compares key metadata across peers and pulls newer records to converge even without reads.
- **KV WAL** ensures data survives restarts; **snapshots** optionally compact state.

---

## Quick start (Windows / PowerShell)

### Start 3 nodes (3 terminals)
```powershell
go run . -id n1 -config nodes.json
go run . -id n2 -config nodes.json
go run . -id n3 -config nodes.json




