// Package events handles firehose event sequencing, persistence, and
// fan-out to WebSocket subscribers for com.atproto.sync.subscribeRepos.
package events

import (
	"bytes"
	"context"
	"fmt"

	atproto "github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/events"
	cbg "github.com/whyrusleeping/cbor-gen"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Persister stores firehose events in the management database.
type Persister struct {
	pool *pgxpool.Pool
}

// NewPersister creates a Persister backed by the management DB pool.
func NewPersister(pool *pgxpool.Pool) *Persister {
	return &Persister{pool: pool}
}

// Persist inserts a commit event into firehose_events and returns the
// assigned sequence number. The BIGSERIAL column provides monotonic ordering.
func (p *Persister) Persist(ctx context.Context, eventType, did string, commit *atproto.SyncSubscribeRepos_Commit) (int64, error) {
	// CBOR-encode the commit payload for storage.
	var buf bytes.Buffer
	if err := commit.MarshalCBOR(&buf); err != nil {
		return 0, fmt.Errorf("persist: marshal commit: %w", err)
	}

	var seq int64
	err := p.pool.QueryRow(ctx,
		`INSERT INTO firehose_events (event_type, did, payload)
		 VALUES ($1, $2, $3)
		 RETURNING seq`,
		eventType, did, buf.Bytes(),
	).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("persist: insert event: %w", err)
	}
	return seq, nil
}

// Replay reads events with seq > since, deserializes each one, sets the
// correct seq, serializes as a wire-format frame (header + payload), and
// calls fn for each frame. Used for cursor-based replay on WebSocket connect.
func (p *Persister) Replay(ctx context.Context, since int64, fn func(frame []byte) error) error {
	rows, err := p.pool.Query(ctx,
		`SELECT seq, payload FROM firehose_events
		 WHERE seq > $1 ORDER BY seq ASC`, since)
	if err != nil {
		return fmt.Errorf("replay: query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var seq int64
		var payload []byte
		if err := rows.Scan(&seq, &payload); err != nil {
			return fmt.Errorf("replay: scan: %w", err)
		}

		// Decode the stored commit payload.
		var commit atproto.SyncSubscribeRepos_Commit
		if err := commit.UnmarshalCBOR(bytes.NewReader(payload)); err != nil {
			return fmt.Errorf("replay: unmarshal seq %d: %w", seq, err)
		}
		commit.Seq = seq

		// Re-serialize as wire frame: header + commit.
		frame, err := encodeFrame(&commit)
		if err != nil {
			return fmt.Errorf("replay: encode seq %d: %w", seq, err)
		}

		if err := fn(frame); err != nil {
			return err
		}
	}
	return rows.Err()
}

// encodeFrame serializes a commit as the AT Protocol firehose wire
// format: CBOR(EventHeader) + CBOR(SyncSubscribeRepos_Commit).
func encodeFrame(commit *atproto.SyncSubscribeRepos_Commit) ([]byte, error) {
	var buf bytes.Buffer
	w := cbg.NewCborWriter(&buf)

	header := events.EventHeader{
		Op:      events.EvtKindMessage,
		MsgType: "#commit",
	}
	if err := header.MarshalCBOR(w); err != nil {
		return nil, fmt.Errorf("encode frame: marshal header: %w", err)
	}
	if err := commit.MarshalCBOR(w); err != nil {
		return nil, fmt.Errorf("encode frame: marshal commit: %w", err)
	}
	return buf.Bytes(), nil
}
