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

## Quickstart (Docker Compose)

This is the easiest way to run a 3-node cluster locally with per-node durable storage.

```bash
make up
make ps
make smoke
```

Write + read via any node:

```bash
curl -X PUT http://localhost:9001/kv/hello -d "world" -v
curl http://localhost:9002/kv/hello -v
curl http://localhost:9003/kv/hello -v
```

Failure demo (hinted handoff + recovery):

```bash
make kill-n2
curl -X PUT http://localhost:9001/kv/a -d "1" -v
make start-n2
# after a moment, n2 should catch up via hinted handoff / anti-entropy
curl http://localhost:9002/kv/a -v
```

Notes:
- Docker uses `nodes.docker.json` (service-name addressing) and runs each node with:
  - `--listen` to bind inside the container
  - `--data_dir=/data` so WAL/snapshots/hints live on a per-node volume

## Local run (no Docker)

Run each node in a separate terminal:

```bash
go run ./cmd/node --id=n1 --config=nodes.json
go run ./cmd/node --id=n2 --config=nodes.json
go run ./cmd/node --id=n3 --config=nodes.json
```


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
### 3) Anti-entropy convergence (cold keys)

**Goal:** a node catches up without any reads.

1. Stop `n3` (Ctrl+C)

2. Write many keys via `n1`:
```powershell
1..50 | % { curl.exe -s -X PUT -d "v$_" "http://127.0.0.1:9001/kv/k$_" | Out-Null }
```

3. Restart `n3`:
```powershell
go run . -id n3 -config nodes.json
```

4. Do NOT read keys. Watch anti-entropy stats increase on `n3`:
```powershell
curl.exe http://127.0.0.1:9003/debug/ae
```

5. Verify one key exists on `n3`:
```powershell
Invoke-RestMethod -Method Post `
  -Uri "http://127.0.0.1:9003/internal/get" `
  -ContentType "application/json" `
  -Body (@{ key = "k17" } | ConvertTo-Json -Compress)
```

### 4) Durability (restart survives)

**Goal:** a node keeps data after restart (KV WAL replay; snapshot optional).

```powershell
curl.exe -i -X PUT -d "persist" http://127.0.0.1:9001/kv/p1

# Restart n1 (Ctrl+C in n1 terminal, then run again)
go run . -id n1 -config nodes.json

curl.exe http://127.0.0.1:9001/kv/p1
curl.exe http://127.0.0.1:9001/debug/persist
```

## Persistence notes
- Each node writes a **KV WAL** on every successful local apply (stores the LWW winner).
- On restart, the node loads an optional snapshot, then replays the WAL.
- Snapshots can be enabled with a periodic timer (if supported by your flags/branch).

## Code map (where to look)
- `internal/ring/` — consistent hashing + vnodes + replica selection  
- `internal/coordinator/` — quorum logic, sloppy quorum, read-repair  
- `internal/hints/` — durable hinted handoff queue + delivery loop  
- `internal/store/` — record type, LWW merge, tombstones, WAL + snapshot  
- `internal/transport/` — internal request/response types + HTTP client  
- `main.go` / `cmd/node/` — HTTP server wiring + background loops  

## Tradeoffs / design choices
- LWW is simple and deterministic but can drop concurrent updates (no vector clocks yet).
- Anti-entropy uses metadata scanning rather than Merkle trees (simpler, less scalable).
- Store is in-memory plus WAL (not a full disk-backed engine).
- Repair loops are bounded to avoid repair storms.

## Roadmap
- Vector clocks + sibling resolution
- Merkle trees for scalable anti-entropy
- Membership / failure detection (gossip)
- Better compaction and streaming snapshotting

