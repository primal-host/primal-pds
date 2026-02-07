package repo

import (
	"context"
	"fmt"
	"io"

	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	ipld "github.com/ipfs/go-ipld-format"
	car "github.com/ipld/go-car"
	carutil "github.com/ipld/go-car/util"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MemBlockstore is an in-memory blockstore that implements the
// blockstore.Blockstore interface required by indigo's MST. It wraps
// an in-memory map and provides helpers to load from and persist to
// Postgres.
type MemBlockstore struct {
	blocks map[string]blocks.Block
}

// NewMemBlockstore creates an empty in-memory blockstore.
func NewMemBlockstore() *MemBlockstore {
	return &MemBlockstore{blocks: make(map[string]blocks.Block, 64)}
}

// Get retrieves a block by CID.
func (m *MemBlockstore) Get(_ context.Context, c cid.Cid) (blocks.Block, error) {
	blk, ok := m.blocks[c.KeyString()]
	if !ok {
		return nil, &ipld.ErrNotFound{Cid: c}
	}
	return blk, nil
}

// Put stores a block.
func (m *MemBlockstore) Put(_ context.Context, blk blocks.Block) error {
	m.blocks[blk.Cid().KeyString()] = blk
	return nil
}

// Has reports whether a block exists.
func (m *MemBlockstore) Has(_ context.Context, c cid.Cid) (bool, error) {
	_, ok := m.blocks[c.KeyString()]
	return ok, nil
}

// GetSize returns the size of a block.
func (m *MemBlockstore) GetSize(_ context.Context, c cid.Cid) (int, error) {
	blk, ok := m.blocks[c.KeyString()]
	if !ok {
		return 0, &ipld.ErrNotFound{Cid: c}
	}
	return len(blk.RawData()), nil
}

// PutMany stores multiple blocks.
func (m *MemBlockstore) PutMany(_ context.Context, blks []blocks.Block) error {
	for _, blk := range blks {
		m.blocks[blk.Cid().KeyString()] = blk
	}
	return nil
}

// AllKeysChan returns a channel of all CIDs in the blockstore.
func (m *MemBlockstore) AllKeysChan(_ context.Context) (<-chan cid.Cid, error) {
	ch := make(chan cid.Cid, len(m.blocks))
	for _, blk := range m.blocks {
		ch <- blk.Cid()
	}
	close(ch)
	return ch, nil
}

// HashOnRead is a no-op (not needed for in-memory store).
func (m *MemBlockstore) HashOnRead(_ bool) {}

// DeleteBlock removes a block by CID.
func (m *MemBlockstore) DeleteBlock(_ context.Context, c cid.Cid) error {
	delete(m.blocks, c.KeyString())
	return nil
}

// LoadBlocks loads all blocks for a DID from Postgres into a new
// MemBlockstore.
func LoadBlocks(ctx context.Context, pool *pgxpool.Pool, did string) (*MemBlockstore, error) {
	rows, err := pool.Query(ctx,
		`SELECT cid, data FROM repo_blocks WHERE did = $1`, did)
	if err != nil {
		return nil, fmt.Errorf("blockstore: load blocks for %s: %w", did, err)
	}
	defer rows.Close()

	bs := NewMemBlockstore()
	for rows.Next() {
		var cidStr string
		var data []byte
		if err := rows.Scan(&cidStr, &data); err != nil {
			return nil, fmt.Errorf("blockstore: scan block: %w", err)
		}

		c, err := cid.Decode(cidStr)
		if err != nil {
			return nil, fmt.Errorf("blockstore: decode cid %q: %w", cidStr, err)
		}

		blk, err := blocks.NewBlockWithCid(data, c)
		if err != nil {
			return nil, fmt.Errorf("blockstore: create block: %w", err)
		}
		bs.blocks[c.KeyString()] = blk
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("blockstore: iterate rows: %w", err)
	}
	return bs, nil
}

// PersistAll writes all in-memory blocks to Postgres. Uses ON CONFLICT
// DO NOTHING since blocks are content-addressed (immutable).
func (m *MemBlockstore) PersistAll(ctx context.Context, pool *pgxpool.Pool, did string) error {
	for _, blk := range m.blocks {
		cidStr := blk.Cid().String()
		_, err := pool.Exec(ctx,
			`INSERT INTO repo_blocks (did, cid, data)
			 VALUES ($1, $2, $3)
			 ON CONFLICT DO NOTHING`,
			did, cidStr, blk.RawData())
		if err != nil {
			return fmt.Errorf("blockstore: persist block %s: %w", cidStr, err)
		}
	}
	return nil
}

// ExportCAR writes ALL blocks as a CAR v1 archive. The commit block is
// written first, followed by all other blocks in arbitrary order.
func (m *MemBlockstore) ExportCAR(w io.Writer, commitCID cid.Cid) error {
	h := &car.CarHeader{
		Roots:   []cid.Cid{commitCID},
		Version: 1,
	}
	if err := car.WriteHeader(h, w); err != nil {
		return fmt.Errorf("blockstore: write car header: %w", err)
	}

	// Write commit block first.
	commitBlk, ok := m.blocks[commitCID.KeyString()]
	if !ok {
		return fmt.Errorf("blockstore: commit block not found: %s", commitCID)
	}
	if err := carutil.LdWrite(w, commitCID.Bytes(), commitBlk.RawData()); err != nil {
		return fmt.Errorf("blockstore: write commit block: %w", err)
	}

	// Write remaining blocks.
	for key, blk := range m.blocks {
		if key == commitCID.KeyString() {
			continue
		}
		if err := carutil.LdWrite(w, blk.Cid().Bytes(), blk.RawData()); err != nil {
			return fmt.Errorf("blockstore: write block %s: %w", blk.Cid(), err)
		}
	}
	return nil
}

// TrackingBlockstore wraps a MemBlockstore and records which CIDs were
// present at creation time vs. added during mutations. After a commit,
// NewBlocks returns only the blocks that were added (the diff), which
// is used to generate the firehose CAR payload.
type TrackingBlockstore struct {
	*MemBlockstore
	preloaded map[string]bool
}

// NewTrackingBlockstore wraps an existing MemBlockstore, snapshotting
// its current keys as "preloaded". Any blocks added after this point
// are considered new.
func NewTrackingBlockstore(bs *MemBlockstore) *TrackingBlockstore {
	pre := make(map[string]bool, len(bs.blocks))
	for k := range bs.blocks {
		pre[k] = true
	}
	return &TrackingBlockstore{
		MemBlockstore: bs,
		preloaded:     pre,
	}
}

// NewBlocks returns blocks that were added after the tracking snapshot.
func (t *TrackingBlockstore) NewBlocks() []blocks.Block {
	var out []blocks.Block
	for k, blk := range t.MemBlockstore.blocks {
		if !t.preloaded[k] {
			out = append(out, blk)
		}
	}
	return out
}

// ExportDiffCAR writes only the new blocks (not preloaded) as a CAR v1
// archive. The commit block is written first.
func (t *TrackingBlockstore) ExportDiffCAR(w io.Writer, commitCID cid.Cid) error {
	h := &car.CarHeader{
		Roots:   []cid.Cid{commitCID},
		Version: 1,
	}
	if err := car.WriteHeader(h, w); err != nil {
		return fmt.Errorf("blockstore: write diff car header: %w", err)
	}

	// Write commit block first.
	commitBlk, ok := t.MemBlockstore.blocks[commitCID.KeyString()]
	if !ok {
		return fmt.Errorf("blockstore: commit block not found: %s", commitCID)
	}
	if err := carutil.LdWrite(w, commitCID.Bytes(), commitBlk.RawData()); err != nil {
		return fmt.Errorf("blockstore: write diff commit block: %w", err)
	}

	// Write remaining new blocks.
	for k, blk := range t.MemBlockstore.blocks {
		if t.preloaded[k] || k == commitCID.KeyString() {
			continue
		}
		if err := carutil.LdWrite(w, blk.Cid().Bytes(), blk.RawData()); err != nil {
			return fmt.Errorf("blockstore: write diff block %s: %w", blk.Cid(), err)
		}
	}
	return nil
}
