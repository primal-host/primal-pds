// Package domain provides the data model and CRUD operations for PDS
// domains. A domain represents a DNS name hosted by this PDS instance;
// user accounts are created as <handle>.<domain>.
package domain

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/primal-host/primal-pds/internal/database"
)

// ErrNotFound is returned when a domain lookup finds no matching row.
var ErrNotFound = errors.New("domain: not found")

// Domain represents a single hosted domain.
type Domain struct {
	ID        int       `json:"id"`
	Domain    string    `json:"domain"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Store provides domain CRUD operations backed by PostgreSQL.
type Store struct {
	db *database.DB
}

// NewStore creates a domain Store.
func NewStore(db *database.DB) *Store {
	return &Store{db: db}
}

// Add inserts a new domain with status "active". Returns ErrNotFound if
// the insert fails unexpectedly, or a wrapped pgx error on constraint
// violations (e.g., duplicate domain).
func (s *Store) Add(ctx context.Context, domainName string) (*Domain, error) {
	var d Domain
	err := s.db.Pool.QueryRow(ctx,
		`INSERT INTO domains (domain) VALUES ($1)
		 RETURNING id, domain, status, created_at, updated_at`,
		domainName,
	).Scan(&d.ID, &d.Domain, &d.Status, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("domain: add %q: %w", domainName, err)
	}
	return &d, nil
}

// List returns all domains ordered by name.
func (s *Store) List(ctx context.Context) ([]Domain, error) {
	rows, err := s.db.Pool.Query(ctx,
		`SELECT id, domain, status, created_at, updated_at
		 FROM domains ORDER BY domain`)
	if err != nil {
		return nil, fmt.Errorf("domain: list: %w", err)
	}
	defer rows.Close()

	domains := []Domain{} // empty slice, not nil (clean JSON: [] not null)
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.ID, &d.Domain, &d.Status, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("domain: list scan: %w", err)
		}
		domains = append(domains, d)
	}
	return domains, rows.Err()
}

// GetByName returns a single domain by its DNS name.
// Returns ErrNotFound if no domain matches.
func (s *Store) GetByName(ctx context.Context, domainName string) (*Domain, error) {
	var d Domain
	err := s.db.Pool.QueryRow(ctx,
		`SELECT id, domain, status, created_at, updated_at
		 FROM domains WHERE domain = $1`,
		domainName,
	).Scan(&d.ID, &d.Domain, &d.Status, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, domainName)
	}
	if err != nil {
		return nil, fmt.Errorf("domain: get %q: %w", domainName, err)
	}
	return &d, nil
}

// Update changes a domain's status. Valid statuses are "active" and
// "disabled". Returns ErrNotFound if the domain does not exist.
func (s *Store) Update(ctx context.Context, domainName, status string) (*Domain, error) {
	var d Domain
	err := s.db.Pool.QueryRow(ctx,
		`UPDATE domains SET status = $1, updated_at = NOW()
		 WHERE domain = $2
		 RETURNING id, domain, status, created_at, updated_at`,
		status, domainName,
	).Scan(&d.ID, &d.Domain, &d.Status, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, domainName)
	}
	if err != nil {
		return nil, fmt.Errorf("domain: update %q: %w", domainName, err)
	}
	return &d, nil
}

// Remove deletes a domain by name. Returns ErrNotFound if the domain
// does not exist.
func (s *Store) Remove(ctx context.Context, domainName string) error {
	result, err := s.db.Pool.Exec(ctx,
		`DELETE FROM domains WHERE domain = $1`, domainName)
	if err != nil {
		return fmt.Errorf("domain: remove %q: %w", domainName, err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, domainName)
	}
	return nil
}

// ListActive returns only domains with status "active", ordered by name.
func (s *Store) ListActive(ctx context.Context) ([]Domain, error) {
	rows, err := s.db.Pool.Query(ctx,
		`SELECT id, domain, status, created_at, updated_at
		 FROM domains WHERE status = 'active' ORDER BY domain`)
	if err != nil {
		return nil, fmt.Errorf("domain: list active: %w", err)
	}
	defer rows.Close()

	domains := []Domain{}
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.ID, &d.Domain, &d.Status, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, fmt.Errorf("domain: list active scan: %w", err)
		}
		domains = append(domains, d)
	}
	return domains, rows.Err()
}
