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
- `internal/database/` — Management DB, tenant pool manager, schema bootstrap
- `internal/domain/` — Domain model, CRUD, and Traefik config generation
- `internal/account/` — Account model, CRUD, DID generation, password hashing
- `internal/repo/` — AT Protocol repository: MST, blockstore, CBOR, signing
- `internal/server/` — Echo HTTP server, routes, and handlers

## Configuration

Loaded from `db.json` in the working directory. See `db.json.example`.

## Database Architecture

Per-domain database isolation for multi-tenant PDS-as-a-service.

**Management database** (`primal_pds`):
- `domains` — domain registry with `db_name` for each tenant database
- `did_routing` — maps DIDs to their home domain for cross-tenant lookups

**Tenant databases** (`primal_pds_<sanitized_domain>`):
- `accounts` — user accounts with signing keys (no domain_id FK)
- `repo_blocks` — content-addressed CBOR blocks per account
- `repo_roots` — current commit head per account repository

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
- Passwords: bcrypt hashed. DIDs: locally-generated did:plc format.
- Each account has a secp256k1 signing key (multibase-encoded)

## Repository

Each account has an AT Protocol repository — a Merkle Search Tree (MST)
of records with signed commits (version 3).

- Records are CBOR-encoded, stored as content-addressed blocks
- Mutations create new signed commits with TID-based revisions
- MST uses indigo's `atproto/repo/mst.Tree`
- Commits use indigo's `atproto/repo.Commit` with secp256k1 signatures
- Manager is stateless — receives tenant pool per operation

## Management API

Namespaced as `host.primal.pds.*` following AT Protocol NSID convention.
Authenticated via `Authorization: Bearer <adminKey>` header.

- `addDomain` — creates domain row, tenant DB, pool, owner account, DID routing
- `removeDomain` — deletes domain row, closes pool, drops tenant DB
- `listAccounts` — requires `?domain=` parameter (queries tenant DB)
- `createAccount` — inserts in tenant DB, adds DID routing row
- `deleteAccount` — removes from tenant DB, deletes DID routing row

## AT Protocol Repo API

Standard `com.atproto.repo.*` endpoints (admin auth for now):
- `createRecord`, `getRecord`, `putRecord`, `deleteRecord`
- `listRecords`, `describeRepo`

Repo resolution uses DID routing table for DIDs, domain extraction for handles.

## Docker

- Container: `primal-pds`
- Networks: `atproto` (Traefik), `infra` (PostgreSQL)
- Config: `./db.json:/app/db.json:ro`
- Traefik dir: `$TRAEFIK_DYNAMIC_DIR:/traefik-dynamic`
