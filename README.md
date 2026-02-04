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
```

### Health checks

```powershell
curl.exe http://127.0.0.1:9001/health
curl.exe http://127.0.0.1:9002/health
curl.exe http://127.0.0.1:9003/health
```

### Basic PUT/GET

```powershell
curl.exe -i -X PUT -d "meow" http://127.0.0.1:9001/kv/cat
curl.exe http://127.0.0.1:9002/kv/cat
```

## Configuration (N/R/W)

- **N** = replication factor (replicas per key)  
- **W** = write quorum (acks required to succeed)  
- **R** = read quorum (responses required to succeed)

Typical setup: **N=3, R=2, W=2** tolerates 1 node down for reads/writes.

`nodes.json` controls:
- node IDs + addresses  
- vnodes per node  
- N/R/W values  

## API

### Client-facing
- `PUT /kv/<key>` (body = bytes)
- `GET /kv/<key>` (returns bytes; 404 if missing/tombstoned)
- `DELETE /kv/<key>` (creates tombstone)

### Internal (node-to-node)
- `POST /internal/put` (replica write; may include hint)
- `POST /internal/get` (replica read)
- `POST /internal/keys` (metadata for anti-entropy)

### Debug
- `GET /debug/hints` (hint queue status)
- `GET /debug/ae` (anti-entropy stats)
- `GET /debug/persist` (WAL/snapshot paths + stats)


## Demo scenarios (failure tests)

### 1) Sloppy quorum + hinted handoff

**Goal:** a write still succeeds when a preferred replica is down, then the down node catches up after restart.

1. Stop node `n2` (Ctrl+C in that terminal)

2. Write through `n1`:
```powershell
curl.exe -i -X PUT -d "v1" http://127.0.0.1:9001/kv/cat
```

3. Restart `n2`:
```powershell
go run . -id n2 -config nodes.json
```

4. Watch hints drain (count should drop over time):
```powershell
curl.exe http://127.0.0.1:9001/debug/hints
```

5. Verify `n2` has the record (internal read):
```powershell
Invoke-RestMethod -Method Post `
  -Uri "http://127.0.0.1:9002/internal/get" `
  -ContentType "application/json" `
  -Body (@{ key = "cat" } | ConvertTo-Json -Compress)
```
### 2) Tombstone delete (no resurrection)

**Goal:** deletes propagate and stay deleted after failures + repairs.

```powershell
curl.exe -i -X PUT -d "meow" http://127.0.0.1:9001/kv/zombie
curl.exe -i -X DELETE http://127.0.0.1:9001/kv/zombie

# Should be 404 everywhere after convergence
curl.exe -i http://127.0.0.1:9002/kv/zombie
curl.exe -i http://127.0.0.1:9003/kv/zombie
```


