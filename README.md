# Mini Dynamo (Go) — Eventually Consistent KV Store

A Dynamo-inspired distributed key-value store built in Go.
Supports quorum reads/writes, sloppy quorum + hinted handoff, tombstones, anti-entropy, and WAL/snapshot durability.

## Why this project
Recruiter version: This project demonstrates distributed systems fundamentals (fault tolerance, consistency tradeoffs, background convergence) with concrete failure-mode demos.

## Features
- Consistent hashing ring + virtual nodes (vnodes)
- Configurable quorum: **N / R / W**
- Sloppy quorum: writes can succeed if a preferred replica is down
- Hinted handoff (durable): missed writes are queued and delivered after recovery
- Tombstones: deletes do not resurrect
- Read-repair on GET
- Anti-entropy: background convergence for “cold keys”
- Durability: per-node WAL replay on restart + optional snapshots

## Architecture diagram (simple)

flowchart LR
  Client[Client] -->|PUT/GET/DELETE /kv/<key>| Coord[Coordinator node]
  Coord --> Ring[Consistent hash ring]

  Coord -->|POST /internal/put| R1[Replica 1]
  Coord -->|POST /internal/put| R2[Replica 2]
  Coord -->|POST /internal/get| R1
  Coord -->|POST /internal/get| R2

  Coord -->|sloppy quorum fallback| FB[Fallback replica]
  FB -->|enqueue hint| HintWAL[Hint WAL]
  HintWAL -->|handoff retry| R1

  AE[Anti-entropy loop] -->|POST /internal/keys| R2
  AE -->|pull newer records| Coord

  subgraph Node storage
    Mem[MemStore (LWW)] --> KVWAL[KV WAL]
    Mem --> Snap[Snapshot]
  end
  Coord --- Mem

