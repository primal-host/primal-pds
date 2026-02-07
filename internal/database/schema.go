// Package database manages the PostgreSQL connection pool and
// bootstraps the schema on startup.
package database

// Schema contains the SQL statements that create all tables needed by the
// PDS. It uses CREATE TABLE IF NOT EXISTS so it is safe to run on every
// startup — existing tables and data are preserved.
//
const Schema = `
-- domains: Each row represents a domain hosted by this PDS instance.
-- Accounts are created under a domain as <handle>.<domain>.
CREATE TABLE IF NOT EXISTS domains (
    id          SERIAL PRIMARY KEY,
    domain      VARCHAR(253) UNIQUE NOT NULL,
    status      VARCHAR(20) NOT NULL DEFAULT 'active',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_domains_status ON domains(status);

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
    domain_id   INTEGER NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
    role        VARCHAR(20) NOT NULL DEFAULT 'user',
    status      VARCHAR(20) NOT NULL DEFAULT 'active',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_accounts_domain_id ON accounts(domain_id);
CREATE INDEX IF NOT EXISTS idx_accounts_status ON accounts(status);
`
