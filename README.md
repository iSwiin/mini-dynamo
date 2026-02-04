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

```mermaid
flowchart LR
  client[Client] --> coord[Coordinator]
  coord --> ring[Consistent hash ring]

  coord --> r1[Replica 1]
  coord --> r2[Replica 2]

  coord --> fb[Fallback replica (sloppy quorum)]
  fb --> hintwal[Hint WAL (durable)]
  hintwal --> r1[Replica 1 (handoff target)]

  coord --> mem[MemStore (LWW)]
  mem --> kvwal[KV WAL]
  mem --> snap[Snapshot (optional)]

  ae[Anti-entropy loop] --> r2
  ae --> coord
```




