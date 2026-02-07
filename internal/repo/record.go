// Package repo provides AT Protocol repository operations: Merkle Search
// Tree (MST) management, content-addressed block storage, commit signing,
// and record CRUD.
package repo

import (
	"github.com/bluesky-social/indigo/atproto/data"

	"github.com/ipfs/go-cid"
	"github.com/multiformats/go-multihash"
)

// EncodeRecord converts a parsed atproto data map to DAG-CBOR bytes.
// The input should already be in the atproto data model (i.e., parsed
// through data.UnmarshalJSON).
func EncodeRecord(record map[string]any) ([]byte, error) {
	return data.MarshalCBOR(record)
}

// DecodeRecord converts DAG-CBOR bytes back to an atproto data map
// suitable for JSON serialization.
func DecodeRecord(cborBytes []byte) (map[string]any, error) {
	return data.UnmarshalCBOR(cborBytes)
}

// ComputeCID returns a CIDv1 (SHA-256, DAG-CBOR codec) for raw bytes.
func ComputeCID(raw []byte) (cid.Cid, error) {
	builder := cid.NewPrefixV1(cid.DagCBOR, multihash.SHA2_256)
	return builder.Sum(raw)
}
