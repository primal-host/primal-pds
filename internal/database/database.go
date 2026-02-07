package database

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a pgx connection pool with application-level helpers.
// In the multi-tenant architecture, each tenant gets its own DB
// instance backed by a separate pool.
type DB struct {
	Pool *pgxpool.Pool
}

// Close shuts down the connection pool. Call this during graceful shutdown.
func (db *DB) Close() {
	db.Pool.Close()
}

// ManagementDB wraps the management database pool (domains + did_routing).
type ManagementDB struct {
	Pool     *pgxpool.Pool
	connBase string // connection string template without database name
}

// OpenManagement connects to the management database, verifies the
// connection, and bootstraps the management schema.
func OpenManagement(ctx context.Context, connString, connBase string) (*ManagementDB, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("database: parse config: %w", err)
	}

	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("database: connect: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: ping: %w", err)
	}

	if _, err := pool.Exec(ctx, ManagementSchema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: bootstrap management schema: %w", err)
	}

	return &ManagementDB{Pool: pool, connBase: connBase}, nil
}

// Close shuts down the management database pool.
func (m *ManagementDB) Close() {
	m.Pool.Close()
}

// CreateTenantDB creates a new PostgreSQL database for a domain.
// CREATE DATABASE cannot run inside a transaction, so this uses a
// direct query on the management pool.
func (m *ManagementDB) CreateTenantDB(ctx context.Context, dbName string) error {
	// Use quoted identifier to prevent SQL injection.
	// pgx doesn't support parameter placeholders for identifiers,
	// so we quote the name directly. The name is generated internally
	// (sanitized domain), not from user input.
	_, err := m.Pool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, dbName))
	if err != nil {
		return fmt.Errorf("database: create tenant db %q: %w", dbName, err)
	}
	return nil
}

// DropTenantDB drops a tenant database. Used on domain removal.
func (m *ManagementDB) DropTenantDB(ctx context.Context, dbName string) error {
	_, err := m.Pool.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, dbName))
	if err != nil {
		return fmt.Errorf("database: drop tenant db %q: %w", dbName, err)
	}
	return nil
}

// InsertDIDRouting records a DID→domain mapping in the management database.
func (m *ManagementDB) InsertDIDRouting(ctx context.Context, did, domainName string) error {
	_, err := m.Pool.Exec(ctx,
		`INSERT INTO did_routing (did, domain) VALUES ($1, $2) ON CONFLICT (did) DO NOTHING`,
		did, domainName)
	if err != nil {
		return fmt.Errorf("database: insert did routing: %w", err)
	}
	return nil
}

// DeleteDIDRouting removes a DID→domain mapping.
func (m *ManagementDB) DeleteDIDRouting(ctx context.Context, did string) error {
	_, err := m.Pool.Exec(ctx, `DELETE FROM did_routing WHERE did = $1`, did)
	if err != nil {
		return fmt.Errorf("database: delete did routing: %w", err)
	}
	return nil
}

// LookupDIDDomain returns the domain for a DID from the routing table.
func (m *ManagementDB) LookupDIDDomain(ctx context.Context, did string) (string, error) {
	var domainName string
	err := m.Pool.QueryRow(ctx,
		`SELECT domain FROM did_routing WHERE did = $1`, did,
	).Scan(&domainName)
	if err != nil {
		return "", fmt.Errorf("database: lookup did domain: %w", err)
	}
	return domainName, nil
}

// PoolManager maps domain names to tenant database connection pools.
type PoolManager struct {
	mu       sync.RWMutex
	pools    map[string]*pgxpool.Pool
	connBase string
}

// NewPoolManager creates an empty pool manager.
func NewPoolManager(connBase string) *PoolManager {
	return &PoolManager{
		pools:    make(map[string]*pgxpool.Pool),
		connBase: connBase,
	}
}

// Get returns the tenant pool for a domain. Returns nil if not found.
func (pm *PoolManager) Get(domainName string) *pgxpool.Pool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.pools[domainName]
}

// Add opens a connection pool for a tenant database, bootstraps the
// tenant schema, and registers it in the pool manager.
func (pm *PoolManager) Add(ctx context.Context, domainName, dbName string) error {
	connStr := pm.connBase + "/" + dbName + "?sslmode=disable"

	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return fmt.Errorf("database: parse tenant config for %q: %w", domainName, err)
	}

	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("database: connect tenant %q: %w", domainName, err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return fmt.Errorf("database: ping tenant %q: %w", domainName, err)
	}

	if _, err := pool.Exec(ctx, TenantSchema); err != nil {
		pool.Close()
		return fmt.Errorf("database: bootstrap tenant schema for %q: %w", domainName, err)
	}

	pm.mu.Lock()
	pm.pools[domainName] = pool
	pm.mu.Unlock()

	return nil
}

// Remove closes and deregisters the tenant pool for a domain.
func (pm *PoolManager) Remove(domainName string) {
	pm.mu.Lock()
	if pool, ok := pm.pools[domainName]; ok {
		pool.Close()
		delete(pm.pools, domainName)
	}
	pm.mu.Unlock()
}

// Close shuts down all tenant pools.
func (pm *PoolManager) Close() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for name, pool := range pm.pools {
		pool.Close()
		delete(pm.pools, name)
	}
}
