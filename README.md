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

### Option A — Mermaid (renders on GitHub)
```mermaid
flowchart LR
  Client -->|PUT/GET/DELETE /kv/{key}| Coord[Node (Coordinator)]
  Coord --> Ring[Consistent Hash Ring]

  Coord -->|/internal/put| R1[Replica]
  Coord -->|/internal/put| R2[Replica]
  Coord -->|/internal/get| R1
  Coord -->|/internal/get| R2

  Coord -->|if preferred down| FB[Fallback Replica]
  FB -->|store hint + record| HW[Hint WAL]
  HW -->|background delivery| R1

  AE[Anti-Entropy Loop] -->|/internal/keys| R2
  AE -->|pull newer records| Coord

  subgraph Each Node
    Store[MemStore (LWW)] --> WAL[KV WAL]
    WAL -->|replay on restart| Store
    Store --> Snap[Snapshot (optional)]
  end
