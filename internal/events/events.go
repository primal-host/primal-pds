package events

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	atproto "github.com/bluesky-social/indigo/api/atproto"
	lexutil "github.com/bluesky-social/indigo/lex/util"

	"github.com/ipfs/go-cid"
)

// CommitInfo carries everything needed to build a firehose commit event.
type CommitInfo struct {
	DID       string
	Rev       string
	PrevRev   string
	CommitCID string
	PrevData  *cid.Cid
	DiffCAR   []byte
	Ops       []OpInfo
	Time      time.Time
}

// OpInfo describes a single record mutation.
type OpInfo struct {
	Action string   // "create", "update", or "delete"
	Path   string   // collection/rkey
	CID    *cid.Cid // new record CID (nil for delete)
	Prev   *cid.Cid // previous record CID (nil for create)
}

// subscriber represents a connected firehose consumer.
type subscriber struct {
	ch   chan []byte
	done chan struct{}
}

// Manager handles event sequencing, persistence, and fan-out to
// WebSocket subscribers.
type Manager struct {
	persister *Persister

	mu   sync.RWMutex
	subs map[*subscriber]struct{}
	done chan struct{}
}

// NewManager creates an EventManager.
func NewManager(persister *Persister) *Manager {
	return &Manager{
		persister: persister,
		subs:      make(map[*subscriber]struct{}),
		done:      make(chan struct{}),
	}
}

// Emit persists a commit event and broadcasts the wire frame to all
// subscribers. Returns error only if persistence fails.
func (m *Manager) Emit(ctx context.Context, info *CommitInfo) error {
	// Build the SyncSubscribeRepos_Commit.
	commitCID, err := cid.Decode(info.CommitCID)
	if err != nil {
		return fmt.Errorf("events: decode commit cid: %w", err)
	}

	ops := make([]*atproto.SyncSubscribeRepos_RepoOp, len(info.Ops))
	for i, op := range info.Ops {
		repoOp := &atproto.SyncSubscribeRepos_RepoOp{
			Action: op.Action,
			Path:   op.Path,
		}
		if op.CID != nil {
			ll := lexutil.LexLink(*op.CID)
			repoOp.Cid = &ll
		}
		if op.Prev != nil {
			ll := lexutil.LexLink(*op.Prev)
			repoOp.Prev = &ll
		}
		ops[i] = repoOp
	}

	var since *string
	if info.PrevRev != "" {
		since = &info.PrevRev
	}

	var prevData *lexutil.LexLink
	if info.PrevData != nil {
		ll := lexutil.LexLink(*info.PrevData)
		prevData = &ll
	}

	commit := &atproto.SyncSubscribeRepos_Commit{
		Repo:     info.DID,
		Rev:      info.Rev,
		Commit:   lexutil.LexLink(commitCID),
		Blocks:   lexutil.LexBytes(info.DiffCAR),
		Ops:      ops,
		Blobs:    []lexutil.LexLink{},
		Since:    since,
		PrevData: prevData,
		Time:     info.Time.UTC().Format(time.RFC3339),
		Rebase:   false,
		TooBig:   false,
	}

	// Persist to get sequence number.
	seq, err := m.persister.Persist(ctx, "commit", info.DID, commit)
	if err != nil {
		return fmt.Errorf("events: persist: %w", err)
	}
	commit.Seq = seq

	// Serialize the wire frame.
	frame, err := encodeFrame(commit)
	if err != nil {
		return fmt.Errorf("events: encode frame: %w", err)
	}

	// Broadcast to subscribers.
	m.broadcast(frame)
	return nil
}

// Subscribe returns a channel of pre-serialized CBOR frames. If since
// is non-nil, events after that cursor are replayed before live frames.
// The returned cancel function must be called when the subscriber is done.
func (m *Manager) Subscribe(ctx context.Context, since *int64) (<-chan []byte, func(), error) {
	sub := &subscriber{
		ch:   make(chan []byte, 256),
		done: make(chan struct{}),
	}

	// Register subscriber BEFORE replay so we don't miss events between
	// replay end and live start.
	m.mu.Lock()
	m.subs[sub] = struct{}{}
	m.mu.Unlock()

	cancel := func() {
		m.mu.Lock()
		delete(m.subs, sub)
		m.mu.Unlock()
		close(sub.done)
	}

	// Replay historical events if cursor provided.
	if since != nil {
		go func() {
			err := m.persister.Replay(ctx, *since, func(frame []byte) error {
				select {
				case sub.ch <- frame:
					return nil
				case <-sub.done:
					return fmt.Errorf("subscriber cancelled")
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			if err != nil {
				log.Printf("Warning: replay error: %v", err)
			}
		}()
	}

	return sub.ch, cancel, nil
}

// Shutdown closes the manager and all subscriber channels.
func (m *Manager) Shutdown() {
	close(m.done)
	m.mu.Lock()
	defer m.mu.Unlock()
	for sub := range m.subs {
		close(sub.ch)
		delete(m.subs, sub)
	}
}

// broadcast sends a frame to all subscribers. Slow consumers whose
// buffers are full get their channel closed (they should reconnect).
func (m *Manager) broadcast(frame []byte) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for sub := range m.subs {
		select {
		case sub.ch <- frame:
		default:
			// Slow consumer — close their channel so they reconnect.
			close(sub.ch)
			go func(s *subscriber) {
				m.mu.Lock()
				delete(m.subs, s)
				m.mu.Unlock()
			}(sub)
		}
	}
}
