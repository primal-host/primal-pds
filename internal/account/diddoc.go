package account

import (
	"fmt"

	"github.com/primal-host/primal-pds/internal/repo"
)

// DIDDocument represents an AT Protocol DID document.
type DIDDocument struct {
	Context            []string             `json:"@context"`
	ID                 string               `json:"id"`
	AlsoKnownAs        []string             `json:"alsoKnownAs"`
	VerificationMethod []VerificationMethod `json:"verificationMethod"`
	Service            []Service            `json:"service"`
}

// VerificationMethod describes a cryptographic key in a DID document.
type VerificationMethod struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	Controller         string `json:"controller"`
	PublicKeyMultibase string `json:"publicKeyMultibase"`
}

// Service describes a service endpoint in a DID document.
type Service struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ServiceEndpoint string `json:"serviceEndpoint"`
}

// BuildDIDDocument constructs an AT Protocol DID document from account
// parameters. The signing key multibase is the private key — the public
// key is derived from it for the verificationMethod.
func BuildDIDDocument(did, handle, signingKeyMultibase, domainName string) (*DIDDocument, error) {
	privKey, err := repo.ParseKey(signingKeyMultibase)
	if err != nil {
		return nil, fmt.Errorf("diddoc: parse signing key: %w", err)
	}

	pubKey, err := privKey.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("diddoc: derive public key: %w", err)
	}
	pubMultibase := pubKey.Multibase()

	return &DIDDocument{
		Context: []string{
			"https://www.w3.org/ns/did/v1",
			"https://w3id.org/security/multikey/v1",
			"https://w3id.org/security/suites/secp256k1-2019/v1",
		},
		ID:          did,
		AlsoKnownAs: []string{"at://" + handle},
		VerificationMethod: []VerificationMethod{
			{
				ID:                 did + "#atproto",
				Type:               "Multikey",
				Controller:         did,
				PublicKeyMultibase: pubMultibase,
			},
		},
		Service: []Service{
			{
				ID:              "#atproto_pds",
				Type:            "AtprotoPersonalDataServer",
				ServiceEndpoint: "https://" + domainName,
			},
		},
	}, nil
}
