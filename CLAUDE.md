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

- `cmd/primal-pds/main.go` — Entry point, wires config + db + server
- `internal/config/` — db.json config loading and validation
- `internal/database/` — PostgreSQL connection pool and schema bootstrap
- `internal/domain/` — Domain model, CRUD, and Traefik config generation
- `internal/account/` — Account model, CRUD, DID generation, password hashing
- `internal/repo/` — AT Protocol repository: MST, blockstore, CBOR, signing
- `internal/server/` — Echo HTTP server, routes, and handlers

## Configuration

Loaded from `db.json` in the working directory. See `db.json.example`.

## Database

PostgreSQL with pgx connection pool. Schema auto-created on startup
using `CREATE TABLE IF NOT EXISTS`.

Tables:
- `domains` — hosted domain configurations
- `accounts` — user accounts with signing keys
- `repo_blocks` — content-addressed CBOR blocks per account (MST nodes, records, commits)
- `repo_roots` — current commit head per account repository

## Account Model

- Roles: `owner` (domain admin, immutable), `admin`, `user`
- Statuses: `active`, `suspended`, `disabled`, `removed`
- Adding a domain auto-creates an owner account with generated password
- Owner accounts cannot be demoted/deleted (remove domain instead)
- Passwords: bcrypt hashed. DIDs: locally-generated did:plc format.
- Each account has a secp256k1 signing key (multibase-encoded)

## Repository

Each account has an AT Protocol repository — a Merkle Search Tree (MST)
of records with signed commits (version 3).

- Records are CBOR-encoded, stored as content-addressed blocks
- Mutations create new signed commits with TID-based revisions
- MST uses indigo's `atproto/repo/mst.Tree`
- Commits use indigo's `atproto/repo.Commit` with secp256k1 signatures

## Management API

Namespaced as `host.primal.pds.*` following AT Protocol NSID convention.
Authenticated via `Authorization: Bearer <adminKey>` header.

## AT Protocol Repo API

Standard `com.atproto.repo.*` endpoints (admin auth for now):
- `createRecord`, `getRecord`, `putRecord`, `deleteRecord`
- `listRecords`, `describeRepo`

## Docker

- Container: `primal-pds`
- Networks: `atproto` (Traefik), `infra` (PostgreSQL)
- Config: `./db.json:/app/db.json:ro`
- Traefik dir: `$TRAEFIK_DYNAMIC_DIR:/traefik-dynamic`
