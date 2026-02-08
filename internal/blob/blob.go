// Package blob provides content-addressed blob storage for AT Protocol
// media (images, etc.). Blobs are stored in the tenant database keyed
// by (did, cid) with a 1MB size limit.
package blob

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"

	"github.com/ipfs/go-cid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multiformats/go-multihash"
)

// MaxBlobSize is the maximum allowed blob size (1MB).
const MaxBlobSize = 1 << 20

// BlobRef is returned after a successful upload.
type BlobRef struct {
	CID      string `json:"cid"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
}

// Store handles blob uploads and retrieval.
type Store struct{}

// NewStore creates a blob Store.
func NewStore() *Store {
	return &Store{}
}

// Upload reads data from r, computes a CID, and stores the blob in the
// tenant database. Returns a BlobRef on success.
func (s *Store) Upload(ctx context.Context, pool *pgxpool.Pool, did, mimeType string, r io.Reader) (*BlobRef, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBlobSize+1))
	if err != nil {
		return nil, fmt.Errorf("blob: read: %w", err)
	}
	if len(data) > MaxBlobSize {
		return nil, fmt.Errorf("blob: exceeds maximum size of %d bytes", MaxBlobSize)
	}

	// Compute CID using SHA-256 with raw codec.
	hash := sha256.Sum256(data)
	mh, err := multihash.Encode(hash[:], multihash.SHA2_256)
	if err != nil {
		return nil, fmt.Errorf("blob: multihash: %w", err)
	}
	c := cid.NewCidV1(cid.Raw, mh)
	cidStr := c.String()

	_, err = pool.Exec(ctx,
		`INSERT INTO blobs (did, cid, mime_type, size, data)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (did, cid) DO NOTHING`,
		did, cidStr, mimeType, len(data), data,
	)
	if err != nil {
		return nil, fmt.Errorf("blob: store: %w", err)
	}

	return &BlobRef{
		CID:      cidStr,
		MimeType: mimeType,
		Size:     int64(len(data)),
	}, nil
}

// Get retrieves a blob by DID and CID. Returns the data and MIME type.
func (s *Store) Get(ctx context.Context, pool *pgxpool.Pool, did, cidStr string) ([]byte, string, error) {
	var data []byte
	var mimeType string
	err := pool.QueryRow(ctx,
		`SELECT data, mime_type FROM blobs WHERE did = $1 AND cid = $2`,
		did, cidStr,
	).Scan(&data, &mimeType)
	if err != nil {
		return nil, "", fmt.Errorf("blob: not found: %w", err)
	}
	return data, mimeType, nil
}
