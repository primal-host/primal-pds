package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/bluesky-social/indigo/atproto/atcrypto"
	"github.com/bluesky-social/indigo/atproto/atdata"
	indigorepo "github.com/bluesky-social/indigo/atproto/repo"
	"github.com/bluesky-social/indigo/atproto/repo/mst"
	"github.com/bluesky-social/indigo/atproto/syntax"

	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Manager orchestrates all repository operations for the PDS.
// It is stateless — each method receives a tenant pool.
type Manager struct{}

// NewManager creates a repo Manager.
func NewManager() *Manager {
	return &Manager{}
}

// RecordEntry represents a single record in a list response.
type RecordEntry struct {
	URI string         `json:"uri"`
	CID string         `json:"cid"`
	Val map[string]any `json:"value"`
}

// repoRoot holds the current commit state for a repository.
type repoRoot struct {
	CommitCID string
	Rev       string
}

// CommitResult captures everything about a commit that downstream
// consumers (like the firehose) need to build event payloads.
type CommitResult struct {
	CommitCID string
	Rev       string
	PrevRev   string
	PrevData  *cid.Cid
	Ops       []RepoOp
	DiffCAR   []byte // CAR v1 with only new blocks
}

// RepoOp describes a single record mutation within a commit.
type RepoOp struct {
	Action string   // "create", "update", or "delete"
	Path   string   // collection/rkey
	CID    *cid.Cid // new record CID (nil for delete)
	Prev   *cid.Cid // previous record CID (nil for create)
}

// InitRepo creates an empty repository for a new account. It creates
// an empty MST, signs an initial commit, and persists the blocks.
// Safe to call multiple times — returns nil if a root already exists.
func (m *Manager) InitRepo(ctx context.Context, pool *pgxpool.Pool, did, signingKey string) error {
	// Check if repo already exists.
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM repo_roots WHERE did = $1)`, did,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("repo: init check: %w", err)
	}
	if exists {
		return nil
	}

	privKey, err := ParseKey(signingKey)
	if err != nil {
		return fmt.Errorf("repo: init: %w", err)
	}

	bs := NewMemBlockstore()
	tree := mst.NewEmptyTree()

	// Write MST blocks (empty tree still produces a root node).
	mstRoot, err := tree.WriteDiffBlocks(ctx, bs)
	if err != nil {
		return fmt.Errorf("repo: init write mst: %w", err)
	}

	// Create and sign the commit.
	clock := syntax.NewTIDClock(0)
	rev := clock.Next().String()

	commit := indigorepo.Commit{
		DID:     did,
		Version: indigorepo.ATPROTO_REPO_VERSION,
		Prev:    nil,
		Data:    *mstRoot,
		Rev:     rev,
	}
	if err := commit.Sign(privKey); err != nil {
		return fmt.Errorf("repo: init sign: %w", err)
	}

	// Encode commit to CBOR and store as a block.
	commitCID, err := storeCommitBlock(bs, &commit)
	if err != nil {
		return fmt.Errorf("repo: init commit block: %w", err)
	}

	// Persist all blocks and set the root.
	if err := bs.PersistAll(ctx, pool, did); err != nil {
		return fmt.Errorf("repo: init persist: %w", err)
	}
	if err := setRoot(ctx, pool, did, commitCID.String(), rev); err != nil {
		return fmt.Errorf("repo: init root: %w", err)
	}

	return nil
}

// CreateRecord adds a record to an account's repository. It generates
// a TID rkey, inserts into the MST, and creates a signed commit.
func (m *Manager) CreateRecord(ctx context.Context, pool *pgxpool.Pool, did, signingKey, collection string, record map[string]any) (uri string, result *CommitResult, err error) {
	clock := syntax.NewTIDClock(0)
	rkey := clock.Next().String()
	return m.PutRecord(ctx, pool, did, signingKey, collection, rkey, record)
}

// GetRecord reads a record from the repo by collection + rkey.
func (m *Manager) GetRecord(ctx context.Context, pool *pgxpool.Pool, did, collection, rkey string) (cidStr string, record map[string]any, err error) {
	bs, tree, _, err := openRepo(ctx, pool, did)
	if err != nil {
		return "", nil, err
	}

	path := collection + "/" + rkey
	recordCID, err := tree.Get([]byte(path))
	if err != nil {
		return "", nil, fmt.Errorf("repo: get record mst: %w", err)
	}
	if recordCID == nil {
		return "", nil, fmt.Errorf("repo: record not found: %s", path)
	}

	blk, err := bs.Get(ctx, *recordCID)
	if err != nil {
		return "", nil, fmt.Errorf("repo: get record block: %w", err)
	}

	rec, err := DecodeRecord(blk.RawData())
	if err != nil {
		return "", nil, fmt.Errorf("repo: decode record: %w", err)
	}

	return recordCID.String(), rec, nil
}

// DeleteRecord removes a record from the repo.
func (m *Manager) DeleteRecord(ctx context.Context, pool *pgxpool.Pool, did, signingKey, collection, rkey string) (*CommitResult, error) {
	privKey, err := ParseKey(signingKey)
	if err != nil {
		return nil, fmt.Errorf("repo: delete: %w", err)
	}

	tbs, tree, root, err := openRepo(ctx, pool, did)
	if err != nil {
		return nil, err
	}

	path := collection + "/" + rkey
	prev, err := tree.Remove([]byte(path))
	if err != nil {
		return nil, fmt.Errorf("repo: delete mst remove: %w", err)
	}
	if prev == nil {
		return nil, fmt.Errorf("repo: record not found: %s", path)
	}

	ops := []RepoOp{{
		Action: "delete",
		Path:   path,
		CID:    nil,
		Prev:   prev,
	}}

	result, err := commitRepo(ctx, pool, did, privKey, tbs, &tree, root, ops)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// PutRecord creates or updates a record at a specific rkey.
func (m *Manager) PutRecord(ctx context.Context, pool *pgxpool.Pool, did, signingKey, collection, rkey string, record map[string]any) (uri string, result *CommitResult, err error) {
	privKey, err := ParseKey(signingKey)
	if err != nil {
		return "", nil, fmt.Errorf("repo: put: %w", err)
	}

	// Parse the JSON record through the atproto data model.
	rawJSON, err := json.Marshal(record)
	if err != nil {
		return "", nil, fmt.Errorf("repo: put marshal json: %w", err)
	}
	parsed, err := atdata.UnmarshalJSON(rawJSON)
	if err != nil {
		return "", nil, fmt.Errorf("repo: put parse record: %w", err)
	}

	// Encode record as DAG-CBOR.
	cborBytes, err := EncodeRecord(parsed)
	if err != nil {
		return "", nil, fmt.Errorf("repo: put encode: %w", err)
	}

	recordCID, err := ComputeCID(cborBytes)
	if err != nil {
		return "", nil, fmt.Errorf("repo: put cid: %w", err)
	}

	tbs, tree, root, err := openRepo(ctx, pool, did)
	if err != nil {
		return "", nil, err
	}

	// Store the record block.
	blk, err := blocks.NewBlockWithCid(cborBytes, recordCID)
	if err != nil {
		return "", nil, fmt.Errorf("repo: put create block: %w", err)
	}
	if err := tbs.Put(ctx, blk); err != nil {
		return "", nil, fmt.Errorf("repo: put store block: %w", err)
	}

	// Insert into MST. prev is non-nil if this is an update.
	path := collection + "/" + rkey
	prev, err := tree.Insert([]byte(path), recordCID)
	if err != nil {
		return "", nil, fmt.Errorf("repo: put mst insert: %w", err)
	}

	action := "create"
	if prev != nil {
		action = "update"
	}
	ops := []RepoOp{{
		Action: action,
		Path:   path,
		CID:    &recordCID,
		Prev:   prev,
	}}

	result, err = commitRepo(ctx, pool, did, privKey, tbs, &tree, root, ops)
	if err != nil {
		return "", nil, err
	}

	atURI := "at://" + did + "/" + collection + "/" + rkey
	return atURI, result, nil
}

// ListRecords returns records in a collection with pagination.
func (m *Manager) ListRecords(ctx context.Context, pool *pgxpool.Pool, did, collection string, limit int, cursor string, reverse bool) ([]RecordEntry, string, error) {
	bs, tree, _, err := openRepo(ctx, pool, did)
	if err != nil {
		return nil, "", err
	}

	prefix := collection + "/"
	var entries []struct {
		key string
		val cid.Cid
	}

	err = tree.Walk(func(key []byte, val cid.Cid) error {
		k := string(key)
		if !strings.HasPrefix(k, prefix) {
			return nil
		}
		entries = append(entries, struct {
			key string
			val cid.Cid
		}{k, val})
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("repo: list walk: %w", err)
	}

	if reverse {
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	}

	// Apply cursor: skip entries until we pass the cursor.
	startIdx := 0
	if cursor != "" {
		cursorPath := prefix + cursor
		for i, e := range entries {
			if e.key == cursorPath {
				startIdx = i + 1
				break
			}
		}
	}

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var records []RecordEntry
	var nextCursor string
	for i := startIdx; i < len(entries) && len(records) < limit; i++ {
		e := entries[i]
		rkey := strings.TrimPrefix(e.key, prefix)

		blk, err := bs.Get(ctx, e.val)
		if err != nil {
			return nil, "", fmt.Errorf("repo: list get block %s: %w", e.val.String(), err)
		}
		rec, err := DecodeRecord(blk.RawData())
		if err != nil {
			return nil, "", fmt.Errorf("repo: list decode: %w", err)
		}

		records = append(records, RecordEntry{
			URI: "at://" + did + "/" + e.key,
			CID: e.val.String(),
			Val: rec,
		})

		if len(records) == limit && i+1 < len(entries) {
			nextCursor = rkey
		}
	}

	return records, nextCursor, nil
}

// DescribeRepo returns the distinct collection NSIDs present in a repo.
func (m *Manager) DescribeRepo(ctx context.Context, pool *pgxpool.Pool, did string) ([]string, error) {
	_, tree, _, err := openRepo(ctx, pool, did)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	err = tree.Walk(func(key []byte, _ cid.Cid) error {
		k := string(key)
		if idx := strings.Index(k, "/"); idx > 0 {
			seen[k[:idx]] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("repo: describe walk: %w", err)
	}

	collections := make([]string, 0, len(seen))
	for c := range seen {
		collections = append(collections, c)
	}
	return collections, nil
}

// GetRoot returns the current commit CID and rev for a DID.
func (m *Manager) GetRoot(ctx context.Context, pool *pgxpool.Pool, did string) (commitCID, rev string, err error) {
	root, err := loadRoot(ctx, pool, did)
	if err != nil {
		return "", "", err
	}
	return root.CommitCID, root.Rev, nil
}

// ExportRepo writes the full repository as a CAR v1 archive to w.
func (m *Manager) ExportRepo(ctx context.Context, pool *pgxpool.Pool, did string, w io.Writer) error {
	root, err := loadRoot(ctx, pool, did)
	if err != nil {
		return fmt.Errorf("repo: export: %w", err)
	}

	bs, err := LoadBlocks(ctx, pool, did)
	if err != nil {
		return fmt.Errorf("repo: export load blocks: %w", err)
	}

	commitCID, err := cid.Decode(root.CommitCID)
	if err != nil {
		return fmt.Errorf("repo: export decode commit cid: %w", err)
	}

	return bs.ExportCAR(w, commitCID)
}

// openRepo loads blocks from Postgres, rebuilds the MST tree, and
// returns a TrackingBlockstore that can distinguish new blocks from
// preloaded ones.
func openRepo(ctx context.Context, pool *pgxpool.Pool, did string) (*TrackingBlockstore, mst.Tree, *repoRoot, error) {
	root, err := loadRoot(ctx, pool, did)
	if err != nil {
		return nil, mst.Tree{}, nil, fmt.Errorf("repo: open load root: %w", err)
	}

	bs, err := LoadBlocks(ctx, pool, did)
	if err != nil {
		return nil, mst.Tree{}, nil, fmt.Errorf("repo: open load blocks: %w", err)
	}

	// Load the commit to find the MST root CID.
	commitCID, err := cid.Decode(root.CommitCID)
	if err != nil {
		return nil, mst.Tree{}, nil, fmt.Errorf("repo: open decode commit cid: %w", err)
	}

	commitBlk, err := bs.Get(ctx, commitCID)
	if err != nil {
		return nil, mst.Tree{}, nil, fmt.Errorf("repo: open get commit block: %w", err)
	}

	var commit indigorepo.Commit
	if err := commit.UnmarshalCBOR(bytes.NewReader(commitBlk.RawData())); err != nil {
		return nil, mst.Tree{}, nil, fmt.Errorf("repo: open unmarshal commit: %w", err)
	}

	tbs := NewTrackingBlockstore(bs)

	tree, err := mst.LoadTreeFromStore(ctx, tbs, commit.Data)
	if err != nil {
		return nil, mst.Tree{}, nil, fmt.Errorf("repo: open load mst: %w", err)
	}

	return tbs, *tree, root, nil
}

// commitRepo signs a new commit, writes MST blocks, generates a diff
// CAR from the TrackingBlockstore, and persists to Postgres. Returns a
// CommitResult containing everything the firehose needs.
func commitRepo(ctx context.Context, pool *pgxpool.Pool, did string, privKey atcrypto.PrivateKey, tbs *TrackingBlockstore, tree *mst.Tree, prevRoot *repoRoot, ops []RepoOp) (*CommitResult, error) {
	// Write dirty MST nodes to blockstore.
	mstRoot, err := tree.WriteDiffBlocks(ctx, tbs)
	if err != nil {
		return nil, fmt.Errorf("repo: commit write mst: %w", err)
	}

	// Build prev CID pointer and capture prevData from old commit.
	var prevCID *cid.Cid
	var prevData *cid.Cid
	var prevRev string
	if prevRoot != nil {
		c, err := cid.Decode(prevRoot.CommitCID)
		if err != nil {
			return nil, fmt.Errorf("repo: commit decode prev: %w", err)
		}
		prevCID = &c
		prevRev = prevRoot.Rev

		// Read old commit to get prevData (the MST root of previous commit).
		oldBlk, err := tbs.Get(ctx, c)
		if err == nil {
			var oldCommit indigorepo.Commit
			if err := oldCommit.UnmarshalCBOR(bytes.NewReader(oldBlk.RawData())); err == nil {
				prevData = &oldCommit.Data
			}
		}
	}

	clock := syntax.NewTIDClock(0)
	rev := clock.Next().String()

	commit := indigorepo.Commit{
		DID:     did,
		Version: indigorepo.ATPROTO_REPO_VERSION,
		Prev:    prevCID,
		Data:    *mstRoot,
		Rev:     rev,
	}
	if err := commit.Sign(privKey); err != nil {
		return nil, fmt.Errorf("repo: commit sign: %w", err)
	}

	commitCID, err := storeCommitBlock(tbs.MemBlockstore, &commit)
	if err != nil {
		return nil, fmt.Errorf("repo: commit store: %w", err)
	}

	// Generate diff CAR from tracking blockstore.
	var diffBuf bytes.Buffer
	if err := tbs.ExportDiffCAR(&diffBuf, commitCID); err != nil {
		return nil, fmt.Errorf("repo: commit diff car: %w", err)
	}

	// Persist all blocks and update root.
	if err := tbs.MemBlockstore.PersistAll(ctx, pool, did); err != nil {
		return nil, fmt.Errorf("repo: commit persist: %w", err)
	}
	if err := setRoot(ctx, pool, did, commitCID.String(), rev); err != nil {
		return nil, fmt.Errorf("repo: commit root: %w", err)
	}

	return &CommitResult{
		CommitCID: commitCID.String(),
		Rev:       rev,
		PrevRev:   prevRev,
		PrevData:  prevData,
		Ops:       ops,
		DiffCAR:   diffBuf.Bytes(),
	}, nil
}

// storeCommitBlock encodes a commit as CBOR and stores it in the blockstore.
func storeCommitBlock(bs *MemBlockstore, commit *indigorepo.Commit) (cid.Cid, error) {
	var buf bytes.Buffer
	if err := commit.MarshalCBOR(&buf); err != nil {
		return cid.Undef, fmt.Errorf("marshal commit cbor: %w", err)
	}
	commitBytes := buf.Bytes()

	commitCID, err := ComputeCID(commitBytes)
	if err != nil {
		return cid.Undef, fmt.Errorf("compute commit cid: %w", err)
	}

	blk, err := blocks.NewBlockWithCid(commitBytes, commitCID)
	if err != nil {
		return cid.Undef, fmt.Errorf("create commit block: %w", err)
	}
	bs.blocks[commitCID.KeyString()] = blk

	return commitCID, nil
}

// loadRoot loads the repo root from Postgres.
func loadRoot(ctx context.Context, pool *pgxpool.Pool, did string) (*repoRoot, error) {
	var root repoRoot
	err := pool.QueryRow(ctx,
		`SELECT commit_cid, rev FROM repo_roots WHERE did = $1`, did,
	).Scan(&root.CommitCID, &root.Rev)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("repo: no repository for %s", did)
	}
	if err != nil {
		return nil, fmt.Errorf("repo: load root: %w", err)
	}
	return &root, nil
}

// setRoot inserts or updates the repo root in Postgres.
func setRoot(ctx context.Context, pool *pgxpool.Pool, did, commitCID, rev string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO repo_roots (did, commit_cid, rev)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (did) DO UPDATE SET commit_cid = $2, rev = $3, updated_at = NOW()`,
		did, commitCID, rev)
	if err != nil {
		return fmt.Errorf("repo: set root: %w", err)
	}
	return nil
}
