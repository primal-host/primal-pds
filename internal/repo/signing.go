package repo

import (
	"fmt"

	"github.com/bluesky-social/indigo/atproto/crypto"
)

// GenerateKey creates a new secp256k1 private key and returns its
// multibase-encoded string for storage.
func GenerateKey() (string, error) {
	priv, err := crypto.GeneratePrivateKeyK256()
	if err != nil {
		return "", fmt.Errorf("signing: generate key: %w", err)
	}
	return priv.Multibase(), nil
}

// ParseKey loads a private key from its multibase-encoded string.
func ParseKey(multibase string) (crypto.PrivateKeyExportable, error) {
	priv, err := crypto.ParsePrivateMultibase(multibase)
	if err != nil {
		return nil, fmt.Errorf("signing: parse key: %w", err)
	}
	return priv, nil
}
