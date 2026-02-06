// Package database manages the PostgreSQL connection pool and
// bootstraps the schema on startup.
package database

// Schema contains the SQL statements that create all tables needed by the
// PDS. It uses CREATE TABLE IF NOT EXISTS so it is safe to run on every
// startup — existing tables and data are preserved.
//
// Phase 1 defines only the domains table. Future phases will add tables
// for accounts, repositories, records, blobs, and sequencer events.
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

-- Fast filtering by status (most queries target active domains).
CREATE INDEX IF NOT EXISTS idx_domains_status ON domains(status);
`
