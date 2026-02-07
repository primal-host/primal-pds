# primal-pds

A Go-based AT Protocol Personal Data Server (PDS) with multi-domain hosting
and management API.

## Features

- **Multi-domain** — Host AT Protocol accounts across multiple domains from
  a single instance
- **Management API** — XRPC-namespaced endpoints for domain and account
  management (no CLI required)
- **AT Protocol Repositories** — Merkle Search Tree (MST) with signed commits,
  content-addressed block storage, and standard repo XRPC endpoints
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

6. **Create a record**:

   ```bash
   curl -X POST http://localhost:3000/xrpc/com.atproto.repo.createRecord \
     -H "Authorization: Bearer YOUR_ADMIN_KEY" \
     -H "Content-Type: application/json" \
     -d '{
       "repo": "example.com",
       "collection": "app.bsky.feed.post",
       "record": {"$type": "app.bsky.feed.post", "text": "Hello!", "createdAt": "2025-01-01T00:00:00Z"}
     }'
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

**Domains:**

| Method | Path | Description |
|--------|------|-------------|
| POST | `/xrpc/host.primal.pds.addDomain` | Add domain + auto-create owner account |
| GET | `/xrpc/host.primal.pds.listDomains` | List all domains |
| POST | `/xrpc/host.primal.pds.updateDomain` | Update domain status |
| POST | `/xrpc/host.primal.pds.removeDomain` | Remove domain (cascades accounts) |

**Accounts:**

| Method | Path | Description |
|--------|------|-------------|
| POST | `/xrpc/host.primal.pds.createAccount` | Create account under a domain |
| GET | `/xrpc/host.primal.pds.listAccounts` | List accounts (`?domain=...`) |
| GET | `/xrpc/host.primal.pds.getAccount` | Get account (`?handle=...` or `?did=...`) |
| POST | `/xrpc/host.primal.pds.updateAccount` | Change status/role |
| POST | `/xrpc/host.primal.pds.deleteAccount` | Delete account |

**Repository:**

| Method | Path | Description |
|--------|------|-------------|
| POST | `/xrpc/com.atproto.repo.createRecord` | Create a record in a repo |
| GET | `/xrpc/com.atproto.repo.getRecord` | Get a record by collection/rkey |
| POST | `/xrpc/com.atproto.repo.putRecord` | Create or update a record |
| POST | `/xrpc/com.atproto.repo.deleteRecord` | Delete a record |
| GET | `/xrpc/com.atproto.repo.listRecords` | List records in a collection |
| GET | `/xrpc/com.atproto.repo.describeRepo` | Describe repo collections |

## Infrastructure

**Requirements:**

- PostgreSQL 17+ (database must exist; tables are auto-created)
- Traefik v3+ with file provider watching a dynamic config directory
- Docker networks: `atproto` (for Traefik routing) and `infra` (for
  PostgreSQL access)

## License

MIT
