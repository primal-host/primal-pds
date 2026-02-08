// Package database manages the PostgreSQL connection pool and
// bootstraps the schema on startup.
package database

// ManagementSchema contains the SQL statements for the management database
// (primal_pds). It stores the domain registry and DID routing table.
const ManagementSchema = `
-- domains: Each row represents a domain hosted by this PDS instance.
-- Accounts are created under a domain as <handle>.<domain>.
-- db_name records the per-tenant database name for this domain.
CREATE TABLE IF NOT EXISTS domains (
    id          SERIAL PRIMARY KEY,
    domain      VARCHAR(253) UNIQUE NOT NULL,
    db_name     VARCHAR(253) NOT NULL,
    status      VARCHAR(20) NOT NULL DEFAULT 'active',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_domains_status ON domains(status);

-- did_routing: Maps DIDs to their home domain for cross-tenant lookups.
-- Populated on account creation, used for DID→domain resolution.
CREATE TABLE IF NOT EXISTS did_routing (
    did     VARCHAR(255) PRIMARY KEY,
    domain  VARCHAR(253) NOT NULL REFERENCES domains(domain) ON DELETE CASCADE
);

-- firehose_events: Sequenced event log for the com.atproto.sync.subscribeRepos
-- firehose. Each row is a CBOR-encoded commit event. The BIGSERIAL seq column
-- provides a monotonically increasing cursor for replay.
CREATE TABLE IF NOT EXISTS firehose_events (
    seq        BIGSERIAL PRIMARY KEY,
    event_type VARCHAR(20) NOT NULL,
    did        VARCHAR(255) NOT NULL,
    payload    BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_firehose_events_seq ON firehose_events(seq);
`

// TenantSchema contains the SQL statements for per-domain tenant databases.
// Each domain gets its own database with these tables.
const TenantSchema = `
-- accounts: User accounts hosted under a domain.
-- The handle is the user's AT Protocol identifier (e.g., "alice.1440.news").
-- The domain admin account uses the bare domain as its handle (e.g., "1440.news").
--
-- Roles:
--   owner — the domain admin account, created automatically with the domain.
--           Cannot be demoted or removed while the domain exists.
--   admin — can manage accounts within the same domain.
--   user  — regular account, can only manage itself.
--
-- Statuses:
--   active    — normal operation, fully functional.
--   suspended — can still post locally but will not sync to relays.
--   disabled  — data preserved but cannot create new posts.
--   removed   — row kept as tombstone; all associated data is deleted.
CREATE TABLE IF NOT EXISTS accounts (
    id          SERIAL PRIMARY KEY,
    did         VARCHAR(255) UNIQUE NOT NULL,
    handle      VARCHAR(253) UNIQUE NOT NULL,
    email       VARCHAR(255),
    password    VARCHAR(255) NOT NULL,
    signing_key VARCHAR(255),
    role        VARCHAR(20) NOT NULL DEFAULT 'user',
    status      VARCHAR(20) NOT NULL DEFAULT 'active',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_accounts_status ON accounts(status);

-- repo_blocks: Content-addressed blocks scoped per account.
-- Stores MST nodes, record data, and commit objects as CBOR bytes.
CREATE TABLE IF NOT EXISTS repo_blocks (
    did   VARCHAR(255) NOT NULL,
    cid   VARCHAR(255) NOT NULL,
    data  BYTEA NOT NULL,
    PRIMARY KEY (did, cid)
);

-- repo_roots: Current commit head per account repository.
CREATE TABLE IF NOT EXISTS repo_roots (
    did         VARCHAR(255) PRIMARY KEY REFERENCES accounts(did) ON DELETE CASCADE,
    commit_cid  VARCHAR(255) NOT NULL,
    rev         VARCHAR(50) NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- blobs: Content-addressed media storage for images and other binary data.
CREATE TABLE IF NOT EXISTS blobs (
    did        VARCHAR(255) NOT NULL,
    cid        VARCHAR(255) NOT NULL,
    mime_type  VARCHAR(255) NOT NULL,
    size       BIGINT NOT NULL,
    data       BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (did, cid)
);
`
