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

sequenceDiagram
  participant C as Client
  participant A as Coordinator
  participant B as Replica
  participant F as Fallback
  C->>A: PUT /kv/<key>
  A->>B: POST /internal/put (preferred)
  alt preferred down
    A->>F: POST /internal/put (fallback + hint)
    Note over F: Hint WAL queued
  end
  A-->>C: 204 (after W acks)

  Coord --- Mem

