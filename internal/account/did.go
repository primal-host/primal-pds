package account

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
)

// GenerateDID creates a new did:plc identifier. The identifier is a
// random 20-byte value encoded as base32 (lowercase, no padding),
// matching the format used by the AT Protocol PLC directory.
//
// Note: This generates a locally-unique DID. Registration with the
// PLC directory (plc.directory) happens in a later phase when
// federation is enabled.
func GenerateDID() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("did: generate random bytes: %w", err)
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return "did:plc:" + strings.ToLower(encoded), nil
}
