package account

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"strings"

	cbg "github.com/whyrusleeping/cbor-gen"

	"github.com/primal-host/primal-pds/internal/repo"
)

// PLCOperation represents a did:plc genesis operation. This is the
// unsigned operation that gets CBOR-encoded to derive the DID.
type PLCOperation struct {
	Type               string     `json:"type"`
	RotationKeys       []string   `json:"rotationKeys"`
	VerificationMethod PLCVerify  `json:"verificationMethods"`
	AlsoKnownAs        []string   `json:"alsoKnownAs"`
	Services           PLCService `json:"services"`
}

// PLCVerify holds the atproto verification method.
type PLCVerify struct {
	Atproto string `json:"atproto"`
}

// PLCService holds the PDS service endpoint.
type PLCService struct {
	AtprotoPDS PLCEndpoint `json:"atproto_pds"`
}

// PLCEndpoint holds a service type and endpoint URL.
type PLCEndpoint struct {
	Type     string `json:"type"`
	Endpoint string `json:"endpoint"`
}

// GeneratePLCDID derives a proper did:plc from a signing key, handle,
// and service endpoint. The process is:
//  1. Construct unsigned genesis operation
//  2. DAG-CBOR encode it (canonical sorted keys)
//  3. SHA-256 hash
//  4. Truncate to 15 bytes
//  5. base32 lowercase no padding
//  6. Prefix with "did:plc:"
//
// Returns the DID and the genesis operation for optional PLC directory
// registration.
func GeneratePLCDID(signingKeyMultibase, handle, serviceEndpoint string) (string, *PLCOperation, error) {
	privKey, err := repo.ParseKey(signingKeyMultibase)
	if err != nil {
		return "", nil, fmt.Errorf("plc: parse key: %w", err)
	}

	pubKey, err := privKey.PublicKey()
	if err != nil {
		return "", nil, fmt.Errorf("plc: derive public key: %w", err)
	}

	didKey := pubKey.DIDKey()

	op := &PLCOperation{
		Type:         "plc_operation",
		RotationKeys: []string{didKey},
		VerificationMethod: PLCVerify{
			Atproto: didKey,
		},
		AlsoKnownAs: []string{"at://" + handle},
		Services: PLCService{
			AtprotoPDS: PLCEndpoint{
				Type:     "AtprotoPersonalDataServer",
				Endpoint: serviceEndpoint,
			},
		},
	}

	// DAG-CBOR encode the operation as a map with sorted keys.
	cborBytes, err := CborEncodePLCOp(op)
	if err != nil {
		return "", nil, fmt.Errorf("plc: cbor encode: %w", err)
	}

	// SHA-256 hash and truncate to 15 bytes.
	hash := sha256.Sum256(cborBytes)
	truncated := hash[:15]

	// base32 lowercase, no padding.
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(truncated)
	did := "did:plc:" + strings.ToLower(encoded)

	return did, op, nil
}

// CborEncodePLCOp encodes a PLC operation as a CBOR map with sorted
// string keys, matching the canonical DAG-CBOR encoding used by the
// PLC directory for DID derivation.
func CborEncodePLCOp(op *PLCOperation) ([]byte, error) {
	var buf bytes.Buffer
	cw := cbg.NewCborWriter(&buf)

	// Map with 5 entries
	if err := cw.WriteMajorTypeHeader(cbg.MajMap, 5); err != nil {
		return nil, err
	}

	// Keys in DAG-CBOR sorted order: alsoKnownAs, rotationKeys, services, type, verificationMethods

	// alsoKnownAs
	if err := writeTextString(cw, "alsoKnownAs"); err != nil {
		return nil, err
	}
	if err := cw.WriteMajorTypeHeader(cbg.MajArray, uint64(len(op.AlsoKnownAs))); err != nil {
		return nil, err
	}
	for _, aka := range op.AlsoKnownAs {
		if err := writeTextString(cw, aka); err != nil {
			return nil, err
		}
	}

	// rotationKeys
	if err := writeTextString(cw, "rotationKeys"); err != nil {
		return nil, err
	}
	if err := cw.WriteMajorTypeHeader(cbg.MajArray, uint64(len(op.RotationKeys))); err != nil {
		return nil, err
	}
	for _, k := range op.RotationKeys {
		if err := writeTextString(cw, k); err != nil {
			return nil, err
		}
	}

	// services (nested map)
	if err := writeTextString(cw, "services"); err != nil {
		return nil, err
	}
	if err := cw.WriteMajorTypeHeader(cbg.MajMap, 1); err != nil {
		return nil, err
	}
	if err := writeTextString(cw, "atproto_pds"); err != nil {
		return nil, err
	}
	if err := cw.WriteMajorTypeHeader(cbg.MajMap, 2); err != nil {
		return nil, err
	}
	if err := writeTextString(cw, "endpoint"); err != nil {
		return nil, err
	}
	if err := writeTextString(cw, op.Services.AtprotoPDS.Endpoint); err != nil {
		return nil, err
	}
	if err := writeTextString(cw, "type"); err != nil {
		return nil, err
	}
	if err := writeTextString(cw, op.Services.AtprotoPDS.Type); err != nil {
		return nil, err
	}

	// type
	if err := writeTextString(cw, "type"); err != nil {
		return nil, err
	}
	if err := writeTextString(cw, op.Type); err != nil {
		return nil, err
	}

	// verificationMethods (nested map)
	if err := writeTextString(cw, "verificationMethods"); err != nil {
		return nil, err
	}
	if err := cw.WriteMajorTypeHeader(cbg.MajMap, 1); err != nil {
		return nil, err
	}
	if err := writeTextString(cw, "atproto"); err != nil {
		return nil, err
	}
	if err := writeTextString(cw, op.VerificationMethod.Atproto); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// SignPLCOperation signs a PLC genesis operation with the given private key
// and returns the base64url-encoded signature (no padding).
func SignPLCOperation(op *PLCOperation, signingKeyMultibase string) (string, error) {
	cborBytes, err := CborEncodePLCOp(op)
	if err != nil {
		return "", fmt.Errorf("plc sign: cbor encode: %w", err)
	}

	privKey, err := repo.ParseKey(signingKeyMultibase)
	if err != nil {
		return "", fmt.Errorf("plc sign: parse key: %w", err)
	}

	sig, err := privKey.HashAndSign(cborBytes)
	if err != nil {
		return "", fmt.Errorf("plc sign: sign: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(sig), nil
}

// writeTextString writes a CBOR text string (major type 3).
func writeTextString(cw *cbg.CborWriter, s string) error {
	if err := cw.WriteMajorTypeHeader(cbg.MajTextString, uint64(len(s))); err != nil {
		return err
	}
	_, err := cw.Write([]byte(s))
	return err
}
