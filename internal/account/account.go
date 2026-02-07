// Package account provides the data model and operations for AT Protocol
// user accounts. Accounts belong to a domain and are identified by a
// DID (decentralized identifier) and a handle (DNS-based username).
//
// Roles control what an account can do within its domain:
//   - owner: the domain admin account, auto-created with the domain
//   - admin: can manage other accounts in the same domain
//   - user:  regular account
//
// Statuses control the account's operational state:
//   - active:    fully functional
//   - suspended: can post locally but data is not synced to relays
//   - disabled:  data preserved but cannot create new content
//   - removed:   tombstone row; all associated data is deleted
package account

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/primal-host/primal-pds/internal/database"
)

// Sentinel errors for account operations.
var (
	ErrNotFound      = errors.New("account: not found")
	ErrHandleTaken   = errors.New("account: handle already taken")
	ErrEmailTaken    = errors.New("account: email already taken")
	ErrOwnerProtected = errors.New("account: owner account cannot be modified this way")
)

// Valid roles.
const (
	RoleOwner = "owner"
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// Valid statuses.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusDisabled  = "disabled"
	StatusRemoved   = "removed"
)

// Account represents a user account hosted under a domain.
type Account struct {
	ID        int       `json:"id"`
	DID       string    `json:"did"`
	Handle    string    `json:"handle"`
	Email     string    `json:"email,omitempty"`
	DomainID  int       `json:"domainId"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CreateParams holds the parameters for creating a new account.
type CreateParams struct {
	Handle   string
	Email    string
	Password string // plaintext, will be hashed
	DomainID int
	Role     string // defaults to "user" if empty
}

// Store provides account CRUD operations backed by PostgreSQL.
type Store struct {
	db *database.DB
}

// NewStore creates an account Store.
func NewStore(db *database.DB) *Store {
	return &Store{db: db}
}

// Create inserts a new account. It generates a DID, hashes the password,
// and stores the account. Returns the created Account (password excluded)
// and the plaintext password if it was auto-generated.
func (s *Store) Create(ctx context.Context, p CreateParams) (*Account, error) {
	did, err := GenerateDID()
	if err != nil {
		return nil, fmt.Errorf("account: create: %w", err)
	}

	hash, err := HashPassword(p.Password)
	if err != nil {
		return nil, fmt.Errorf("account: create: %w", err)
	}

	role := p.Role
	if role == "" {
		role = RoleUser
	}

	var a Account
	err = s.db.Pool.QueryRow(ctx,
		`INSERT INTO accounts (did, handle, email, password, domain_id, role)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, did, handle, email, domain_id, role, status, created_at, updated_at`,
		did, p.Handle, p.Email, hash, p.DomainID, role,
	).Scan(&a.ID, &a.DID, &a.Handle, &a.Email, &a.DomainID, &a.Role, &a.Status, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("account: create %q: %w", p.Handle, err)
	}
	return &a, nil
}

// GetByHandle returns an account by its handle.
// Returns ErrNotFound if no account matches.
func (s *Store) GetByHandle(ctx context.Context, handle string) (*Account, error) {
	var a Account
	err := s.db.Pool.QueryRow(ctx,
		`SELECT id, did, handle, email, domain_id, role, status, created_at, updated_at
		 FROM accounts WHERE handle = $1`,
		handle,
	).Scan(&a.ID, &a.DID, &a.Handle, &a.Email, &a.DomainID, &a.Role, &a.Status, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, handle)
	}
	if err != nil {
		return nil, fmt.Errorf("account: get by handle %q: %w", handle, err)
	}
	return &a, nil
}

// GetByDID returns an account by its DID.
// Returns ErrNotFound if no account matches.
func (s *Store) GetByDID(ctx context.Context, did string) (*Account, error) {
	var a Account
	err := s.db.Pool.QueryRow(ctx,
		`SELECT id, did, handle, email, domain_id, role, status, created_at, updated_at
		 FROM accounts WHERE did = $1`,
		did,
	).Scan(&a.ID, &a.DID, &a.Handle, &a.Email, &a.DomainID, &a.Role, &a.Status, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, did)
	}
	if err != nil {
		return nil, fmt.Errorf("account: get by did %q: %w", did, err)
	}
	return &a, nil
}

// List returns all accounts, optionally filtered by domain ID.
// Pass domainID <= 0 to list all accounts across all domains.
func (s *Store) List(ctx context.Context, domainID int) ([]Account, error) {
	var query string
	var args []any

	if domainID > 0 {
		query = `SELECT id, did, handle, email, domain_id, role, status, created_at, updated_at
				 FROM accounts WHERE domain_id = $1 ORDER BY handle`
		args = []any{domainID}
	} else {
		query = `SELECT id, did, handle, email, domain_id, role, status, created_at, updated_at
				 FROM accounts ORDER BY handle`
	}

	rows, err := s.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("account: list: %w", err)
	}
	defer rows.Close()

	accounts := []Account{}
	for rows.Next() {
		var a Account
		if err := rows.Scan(&a.ID, &a.DID, &a.Handle, &a.Email, &a.DomainID, &a.Role, &a.Status, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, fmt.Errorf("account: list scan: %w", err)
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// UpdateStatus changes an account's status. The owner account cannot be
// set to "removed" — use domain removal instead.
func (s *Store) UpdateStatus(ctx context.Context, handle, status string) (*Account, error) {
	// Protect owner accounts from removal.
	if status == StatusRemoved {
		existing, err := s.GetByHandle(ctx, handle)
		if err != nil {
			return nil, err
		}
		if existing.Role == RoleOwner {
			return nil, fmt.Errorf("%w: cannot remove owner account directly, remove the domain instead", ErrOwnerProtected)
		}
	}

	var a Account
	err := s.db.Pool.QueryRow(ctx,
		`UPDATE accounts SET status = $1, updated_at = NOW()
		 WHERE handle = $2
		 RETURNING id, did, handle, email, domain_id, role, status, created_at, updated_at`,
		status, handle,
	).Scan(&a.ID, &a.DID, &a.Handle, &a.Email, &a.DomainID, &a.Role, &a.Status, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, handle)
	}
	if err != nil {
		return nil, fmt.Errorf("account: update status %q: %w", handle, err)
	}
	return &a, nil
}

// UpdateRole changes an account's role within its domain. The owner role
// cannot be assigned or removed through this method — it is set only
// during domain creation.
func (s *Store) UpdateRole(ctx context.Context, handle, role string) (*Account, error) {
	if role == RoleOwner {
		return nil, fmt.Errorf("%w: cannot promote to owner", ErrOwnerProtected)
	}

	// Prevent demoting an owner.
	existing, err := s.GetByHandle(ctx, handle)
	if err != nil {
		return nil, err
	}
	if existing.Role == RoleOwner {
		return nil, fmt.Errorf("%w: cannot change owner role", ErrOwnerProtected)
	}

	var a Account
	err = s.db.Pool.QueryRow(ctx,
		`UPDATE accounts SET role = $1, updated_at = NOW()
		 WHERE handle = $2
		 RETURNING id, did, handle, email, domain_id, role, status, created_at, updated_at`,
		role, handle,
	).Scan(&a.ID, &a.DID, &a.Handle, &a.Email, &a.DomainID, &a.Role, &a.Status, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, handle)
	}
	if err != nil {
		return nil, fmt.Errorf("account: update role %q: %w", handle, err)
	}
	return &a, nil
}

// Delete permanently removes an account. Owner accounts cannot be
// deleted directly — remove the domain instead (CASCADE will handle it).
func (s *Store) Delete(ctx context.Context, handle string) error {
	existing, err := s.GetByHandle(ctx, handle)
	if err != nil {
		return err
	}
	if existing.Role == RoleOwner {
		return fmt.Errorf("%w: cannot delete owner account directly, remove the domain instead", ErrOwnerProtected)
	}

	result, err := s.db.Pool.Exec(ctx,
		`DELETE FROM accounts WHERE handle = $1`, handle)
	if err != nil {
		return fmt.Errorf("account: delete %q: %w", handle, err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, handle)
	}
	return nil
}

// ResolveHandle looks up the DID for a given handle. This is used by
// the /.well-known/atproto-did endpoint. Only returns DIDs for active
// accounts.
func (s *Store) ResolveHandle(ctx context.Context, handle string) (string, error) {
	var did string
	err := s.db.Pool.QueryRow(ctx,
		`SELECT did FROM accounts WHERE handle = $1 AND status != 'removed'`,
		handle,
	).Scan(&did)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrNotFound, handle)
	}
	if err != nil {
		return "", fmt.Errorf("account: resolve handle %q: %w", handle, err)
	}
	return did, nil
}

// VerifyPassword checks the password for an account identified by
// handle. Returns the Account on success or an error if the handle is
// not found or the password doesn't match.
func (s *Store) VerifyPassword(ctx context.Context, handle, password string) (*Account, error) {
	var a Account
	var hash string
	err := s.db.Pool.QueryRow(ctx,
		`SELECT id, did, handle, email, password, domain_id, role, status, created_at, updated_at
		 FROM accounts WHERE handle = $1`,
		handle,
	).Scan(&a.ID, &a.DID, &a.Handle, &a.Email, &hash, &a.DomainID, &a.Role, &a.Status, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, handle)
	}
	if err != nil {
		return nil, fmt.Errorf("account: verify password %q: %w", handle, err)
	}

	if err := CheckPassword(hash, password); err != nil {
		return nil, fmt.Errorf("account: invalid password for %q", handle)
	}
	return &a, nil
}
