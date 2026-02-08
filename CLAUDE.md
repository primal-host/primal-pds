# primal-pds

Multi-tenant AT Protocol PDS written in Go. Uses Echo v4 for HTTP,
pgx v5 for PostgreSQL, indigo for AT Protocol repo operations, and
generates Traefik dynamic config for automatic TLS routing.

## Build & Run

```bash
# Local development
go build -o primal-pds ./cmd/primal-pds
./primal-pds

# Docker
./.launch.sh                    # Build and run
./.launch.sh "commit message"   # Commit, push, then build and run

# Checks
go fmt ./...
go vet ./...
```

## Project Structure

- `cmd/primal-pds/main.go` — Entry point, wires config + db + events + auth + server
- `internal/config/` — db.json config loading and validation
- `internal/database/` — Management DB, tenant pool manager, schema bootstrap
- `internal/domain/` — Domain model, CRUD, and Traefik config generation
- `internal/account/` — Account model, CRUD, DID generation, DID documents, PLC derivation
- `internal/auth/` — JWT token management (HS256 access/refresh tokens)
- `internal/blob/` — Content-addressed blob storage (images, media)
- `internal/identity/` — PLC directory registration, relay announcement
- `internal/repo/` — AT Protocol repository: MST, blockstore, CBOR, signing, CAR export
- `internal/events/` — Firehose event manager: persistence, sequencing, fan-out
- `internal/server/` — Echo HTTP server, routes, handlers, WebSocket firehose

## Configuration

Loaded from `db.json` in the working directory. See `db.json.example`.

Optional fields:
- `plcEndpoint` — PLC directory URL. When set, new accounts get proper
  `did:plc` identifiers derived from their signing key instead of random DIDs.
- `serviceURL` — Public URL of this PDS (e.g., `https://pds.primal.host`).
  Used as JWT issuer and for `did:web` in describeServer.
- `jwtSecret` — HMAC secret for JWT signing. Auto-generated if empty (tokens
  won't survive restart).
- `registrationOpen` — When true, `com.atproto.server.createAccount` is open
  to the public. When false (default), only admin key holders can create accounts.

## Database Architecture

Per-domain database isolation for multi-tenant PDS-as-a-service.

**Management database** (`primal_pds`):
- `domains` — domain registry with `db_name` for each tenant database
- `did_routing` — maps DIDs to their home domain for cross-tenant lookups
- `firehose_events` — sequenced event log (BIGSERIAL seq) for subscribeRepos

**Tenant databases** (`primal_pds_<sanitized_domain>`):
- `accounts` — user accounts with signing keys (no domain_id FK)
- `repo_blocks` — content-addressed CBOR blocks per account
- `repo_roots` — current commit head per account repository
- `blobs` — content-addressed media storage (images, etc.)

Domain name → database name: `primal_pds_` + domain with dots→underscores.
Example: `1440.news` → `primal_pds_1440_news`

The `dba_primal_pds` Postgres role needs `CREATEDB` privilege:
```sql
ALTER ROLE dba_primal_pds CREATEDB;
```

## Account Model

- Roles: `owner` (domain admin, immutable), `admin`, `user`
- Statuses: `active`, `suspended`, `disabled`, `removed`
- Adding a domain auto-creates an owner account with generated password
- Owner accounts cannot be demoted/deleted (remove domain instead)
- Passwords: bcrypt hashed
- DIDs: random `did:plc` by default; proper PLC derivation when `plcEndpoint` configured
- Each account has a secp256k1 signing key (multibase-encoded)
- DID documents built from signing key with Multikey verification method

## Authentication

Two auth mechanisms, used by different route groups:

**Admin key** — `Authorization: Bearer <adminKey>` for management API.
**JWT tokens** — HS256 signed, issued by `createSession` (login).
- Access tokens: 2h TTL, scope `com.atproto.access`
- Refresh tokens: 90d TTL, scope `com.atproto.refresh`
- Stateless (no revocation table for MVP)

**Route groups:**
- Public: reads, discovery, createSession, sync, blobs
- Refresh token: refreshSession, deleteSession
- Access token or admin key: getSession, createAccount, repo writes, uploadBlob
- Admin only: management API (host.primal.pds.*)

**Repo authorization:** JWT users can only write to their own repo. Admins
can write to any repo.

## Repository

Each account has an AT Protocol repository — a Merkle Search Tree (MST)
of records with signed commits (version 3).

- Records are CBOR-encoded, stored as content-addressed blocks
- Mutations create new signed commits with TID-based revisions
- MST uses indigo's `atproto/repo/mst.Tree`
- Commits use indigo's `atproto/repo.Commit` with secp256k1 signatures
- Manager is stateless — receives tenant pool per operation
- TrackingBlockstore tracks new vs preloaded blocks for diff CAR generation
- CommitResult captures commit CID, rev, ops, and diff CAR for firehose

## Sync & Federation

AT Protocol sync layer for data export and real-time event streaming.

**CAR Export:**
- `ExportCAR` — full repo export (all blocks) for `getRepo`
- `ExportDiffCAR` — new blocks only, used in firehose commit events

**Firehose (subscribeRepos):**
- EventManager persists events to `firehose_events` (BIGSERIAL seq)
- Broadcasts pre-serialized CBOR frames to WebSocket subscribers
- Cursor-based replay: connect with `?cursor=N` to replay from seq N
- Slow consumers get disconnected (buffer overflow closes channel)
- Wire format: `CBOR(EventHeader) + CBOR(SyncSubscribeRepos_Commit)`

**Identity & Discovery:**
- `resolveHandle` — handle → DID via XRPC endpoint
- PLC directory registration — signs genesis ops, POSTs to plc.directory
- Relay announcement — requestCrawl to bsky.network on startup
- `describeServer` — returns `did:web`, available domains
- `/.well-known/atproto-did` — handle resolution via Host header

**Blob Support:**
- Upload: `uploadBlob` (auth required, 1MB limit, SHA-256 CID)
- Download: `getBlob` (public, by DID + CID)
- Storage: per-tenant `blobs` table (BYTEA)

**DID Documents:**
- Built from account signing key + domain
- Multikey verification method with secp256k1 public key
- AtprotoPersonalDataServer service endpoint

## AT Protocol Session API

Standard `com.atproto.server.*` endpoints:
- `createSession` — login with handle/DID + password, returns JWT pair
- `refreshSession` — new token pair from refresh token
- `getSession` — current session info + DID document
- `deleteSession` — no-op (stateless MVP)
- `describeServer` — service DID, available domains
- `createAccount` — public account creation (gated by `registrationOpen`)

## Management API

Namespaced as `host.primal.pds.*` following AT Protocol NSID convention.
Authenticated via `Authorization: Bearer <adminKey>` header.

- `addDomain` — creates domain row, tenant DB, pool, owner account, DID routing
- `removeDomain` — deletes domain row, closes pool, drops tenant DB
- `listAccounts` — requires `?domain=` parameter (queries tenant DB)
- `createAccount` — inserts in tenant DB, adds DID routing row
- `deleteAccount` — removes from tenant DB, deletes DID routing row

## AT Protocol Repo API

Standard `com.atproto.repo.*` endpoints:
- `createRecord`, `putRecord`, `deleteRecord` — auth required, per-account authorization
- `getRecord`, `listRecords`, `describeRepo` — public reads
- `uploadBlob` — auth required, 1MB limit

Repo resolution uses DID routing table for DIDs, domain extraction for handles.

## AT Protocol Sync API

Public endpoints (no auth):
- `com.atproto.sync.getRepo` — streams full repo as CAR v1
- `com.atproto.sync.getLatestCommit` — returns `{cid, rev}`
- `com.atproto.sync.subscribeRepos` — WebSocket firehose (CBOR frames)
- `com.atproto.sync.getBlob` — retrieve blob by DID + CID
- `com.atproto.sync.requestCrawl` — accept relay crawl requests

## Docker

- Container: `primal-pds`
- Networks: `atproto` (Traefik), `infra` (PostgreSQL)
- Config: `./db.json:/app/db.json:ro`
- Traefik dir: `$TRAEFIK_DYNAMIC_DIR:/traefik-dynamic`
