# primal-pds

A Go-based AT Protocol Personal Data Server (PDS) with multi-domain hosting
and management API.

## Features

- **Multi-domain** — Host AT Protocol accounts across multiple domains from
  a single instance
- **Management API** — XRPC-namespaced endpoints for domain and account
  management (no CLI required)
- **Traefik integration** — Automatically generates dynamic routing config
  with wildcard TLS certificates
- **PostgreSQL** — Centralized storage with auto-schema bootstrap on first run
- **Docker-ready** — Multi-stage build, Traefik + Postgres networking out of
  the box

## Quick Start

1. **Create the database** (one-time):

   ```sql
   CREATE USER dba_primal_pds WITH PASSWORD 'your-password';
   CREATE DATABASE primal_pds OWNER dba_primal_pds;
   \c primal_pds
   GRANT ALL ON SCHEMA public TO dba_primal_pds;
   ```

2. **Configure**:

   ```bash
   cp db.json.example db.json
   # Edit db.json with your database credentials and admin key
   ```

3. **Create a `.env` file** for Docker:

   ```bash
   echo 'TRAEFIK_DYNAMIC_DIR=/path/to/traefik/dynamic' > .env
   ```

4. **Run**:

   ```bash
   ./.launch.sh
   ```

5. **Add a domain**:

   ```bash
   curl -X POST http://localhost:3000/xrpc/host.primal.pds.addDomain \
     -H "Authorization: Bearer YOUR_ADMIN_KEY" \
     -H "Content-Type: application/json" \
     -d '{"domain": "example.com"}'
   ```

## Configuration

All configuration is in `db.json`:

| Field | Description | Default |
|-------|-------------|---------|
| `dbConn` | PostgreSQL host:port | *(required)* |
| `dbName` | Database name | *(required)* |
| `dbUser` | Database username | *(required)* |
| `dbPass` | Database password | *(required)* |
| `listenAddr` | HTTP listen address | `:3000` |
| `traefikConfigDir` | Traefik dynamic config directory | *(required)* |
| `adminKey` | Bearer token for management API | *(required)* |

## API

### Public

| Method | Path | Description |
|--------|------|-------------|
| GET | `/xrpc/_health` | Health check |
| GET | `/.well-known/atproto-did` | AT Protocol DID resolution |

### Management (requires `Authorization: Bearer <adminKey>`)

| Method | Path | Description |
|--------|------|-------------|
| POST | `/xrpc/host.primal.pds.addDomain` | Add a hosted domain |
| GET | `/xrpc/host.primal.pds.listDomains` | List all domains |
| POST | `/xrpc/host.primal.pds.updateDomain` | Update domain status |
| POST | `/xrpc/host.primal.pds.removeDomain` | Remove a domain |

## Infrastructure

**Requirements:**

- PostgreSQL 17+ (database must exist; tables are auto-created)
- Traefik v3+ with file provider watching a dynamic config directory
- Docker networks: `atproto` (for Traefik routing) and `infra` (for
  PostgreSQL access)

## License

MIT
