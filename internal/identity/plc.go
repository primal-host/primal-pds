// Package identity provides PLC directory registration and relay
// announcement for AT Protocol federation.
package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/primal-host/primal-pds/internal/account"
)

// RegisterDID submits a signed genesis operation to the PLC directory
// to register a DID. Non-fatal: logs errors rather than failing.
func RegisterDID(ctx context.Context, plcEndpoint, did string, op *account.PLCOperation, signingKeyMultibase string) error {
	sig, err := account.SignPLCOperation(op, signingKeyMultibase)
	if err != nil {
		return fmt.Errorf("identity: sign plc op: %w", err)
	}

	// Build the signed operation payload.
	payload := map[string]any{
		"type":                op.Type,
		"rotationKeys":       op.RotationKeys,
		"verificationMethods": op.VerificationMethod,
		"alsoKnownAs":        op.AlsoKnownAs,
		"services":           op.Services,
		"sig":                sig,
		"prev":               nil,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("identity: marshal plc op: %w", err)
	}

	url := plcEndpoint + "/" + did
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("identity: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("identity: POST %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Printf("PLC registered: %s at %s", did, plcEndpoint)
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("identity: PLC register %s returned %d: %s", did, resp.StatusCode, string(respBody))
}

// AnnounceToRelay sends a requestCrawl to a relay so it discovers this PDS.
func AnnounceToRelay(ctx context.Context, relayURL, serviceURL string) error {
	payload, _ := json.Marshal(map[string]string{
		"hostname": serviceURL,
	})

	url := relayURL + "/xrpc/com.atproto.sync.requestCrawl"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("identity: create relay request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("identity: announce to relay %s: %w", relayURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Printf("Relay announcement accepted: %s -> %s", serviceURL, relayURL)
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	log.Printf("Relay announcement to %s returned %d: %s", relayURL, resp.StatusCode, string(respBody))
	return nil
}
