# primal-pds

Multi-tenant AT Protocol PDS written in Go. Uses Echo v4 for HTTP,
pgx v5 for PostgreSQL, and generates Traefik dynamic config for
automatic TLS routing.

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
- `internal/server/` — Echo HTTP server, routes, and handlers

## Configuration

Loaded from `db.json` in the working directory. See `db.json.example`.

## Database

PostgreSQL with pgx connection pool. Schema auto-created on startup
using `CREATE TABLE IF NOT EXISTS`.

## Management API

Namespaced as `host.primal.pds.*` following AT Protocol NSID convention.
Authenticated via `Authorization: Bearer <adminKey>` header.

## Docker

- Container: `primal-pds`
- Networks: `atproto` (Traefik), `infra` (PostgreSQL)
- Config: `./db.json:/app/db.json:ro`
- Traefik dir: `$TRAEFIK_DYNAMIC_DIR:/traefik-dynamic`
