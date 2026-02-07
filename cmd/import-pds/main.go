// import-pds copies accounts and records from a source AT Protocol PDS
// into a target primal-pds instance. It uses standard XRPC endpoints to
// read from the source and the primal-pds management API to write to the
// target.
//
// Usage:
//
//	import-pds -source https://1440.news -target http://localhost:3333 \
//	           -admin-key test-admin-key -domain 1440.news
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func main() {
	source := flag.String("source", "", "Source PDS URL (e.g., https://1440.news)")
	target := flag.String("target", "", "Target primal-pds URL (e.g., http://localhost:3333)")
	adminKey := flag.String("admin-key", "", "Target PDS admin key")
	domain := flag.String("domain", "", "Domain to import as (e.g., 1440.news)")
	maxRepos := flag.Int("max-repos", 0, "Limit number of repos to import (0 = all)")
	maxRecords := flag.Int("max-records", 0, "Limit records per account per collection (0 = all)")
	dryRun := flag.Bool("dry-run", false, "List repos and record counts without importing")
	flag.Parse()

	if *source == "" || *target == "" || *adminKey == "" || *domain == "" {
		log.Fatal("All flags are required: -source, -target, -admin-key, -domain")
	}

	imp := &Importer{
		source:     strings.TrimRight(*source, "/"),
		target:     strings.TrimRight(*target, "/"),
		adminKey:   *adminKey,
		domain:     *domain,
		maxRepos:   *maxRepos,
		maxRecords: *maxRecords,
		dryRun:     *dryRun,
		client:     &http.Client{Timeout: 30 * time.Second},
	}

	if err := imp.Run(); err != nil {
		log.Fatalf("Import failed: %v", err)
	}
}

// Importer copies data from a source PDS to a target primal-pds.
type Importer struct {
	source     string
	target     string
	adminKey   string
	domain     string
	maxRepos   int
	maxRecords int
	dryRun     bool
	client     *http.Client
	stats      Stats
}

// Stats tracks import progress.
type Stats struct {
	ReposFound      int
	AccountsCreated int
	AccountsSkipped int
	RecordsCopied   int
	RecordsSkipped  int
	Errors          int
}

// Run performs the full import.
func (imp *Importer) Run() error {
	log.Printf("Import: %s -> %s (domain: %s, dry-run: %v)", imp.source, imp.target, imp.domain, imp.dryRun)

	// Step 1: Add domain to target (skip in dry-run).
	if !imp.dryRun {
		if err := imp.addDomain(); err != nil {
			// Domain might already exist, that's OK.
			if strings.Contains(err.Error(), "DomainExists") {
				log.Printf("Domain %s already exists on target, continuing", imp.domain)
			} else {
				return fmt.Errorf("add domain: %w", err)
			}
		} else {
			log.Printf("Domain %s created on target", imp.domain)
		}
	}

	// Step 2: List all repos from source.
	repos, err := imp.listRepos()
	if err != nil {
		return fmt.Errorf("list repos: %w", err)
	}
	imp.stats.ReposFound = len(repos)
	log.Printf("Found %d repos on source PDS", len(repos))

	if imp.maxRepos > 0 && len(repos) > imp.maxRepos {
		repos = repos[:imp.maxRepos]
		log.Printf("Limiting to %d repos", imp.maxRepos)
	}

	// Step 3: For each repo, describe, create account, copy records.
	for i, r := range repos {
		log.Printf("[%d/%d] Processing %s ...", i+1, len(repos), r.DID)

		desc, err := imp.describeRepo(r.DID)
		if err != nil {
			log.Printf("  Error describing repo %s: %v", r.DID, err)
			imp.stats.Errors++
			continue
		}

		log.Printf("  Handle: %s, Collections: %v", desc.Handle, desc.Collections)

		if imp.dryRun {
			imp.dryRunRepo(desc)
			continue
		}

		// Create account on target.
		if err := imp.createAccount(desc.Handle); err != nil {
			if strings.Contains(err.Error(), "HandleTaken") {
				log.Printf("  Account %s already exists, skipping creation", desc.Handle)
				imp.stats.AccountsSkipped++
			} else {
				log.Printf("  Error creating account %s: %v", desc.Handle, err)
				imp.stats.Errors++
				continue
			}
		} else {
			log.Printf("  Account created: %s", desc.Handle)
			imp.stats.AccountsCreated++
		}

		// Copy records for each collection.
		for _, coll := range desc.Collections {
			copied, skipped, err := imp.copyRecords(r.DID, desc.Handle, coll)
			if err != nil {
				log.Printf("  Error copying %s: %v", coll, err)
				imp.stats.Errors++
				continue
			}
			imp.stats.RecordsCopied += copied
			imp.stats.RecordsSkipped += skipped
			if copied > 0 || skipped > 0 {
				log.Printf("  %s: %d copied, %d skipped", coll, copied, skipped)
			}
		}
	}

	// Print summary.
	fmt.Println()
	fmt.Println("=== Import Summary ===")
	fmt.Printf("Repos found:      %d\n", imp.stats.ReposFound)
	fmt.Printf("Accounts created: %d\n", imp.stats.AccountsCreated)
	fmt.Printf("Accounts skipped: %d\n", imp.stats.AccountsSkipped)
	fmt.Printf("Records copied:   %d\n", imp.stats.RecordsCopied)
	fmt.Printf("Records skipped:  %d\n", imp.stats.RecordsSkipped)
	fmt.Printf("Errors:           %d\n", imp.stats.Errors)

	return nil
}

// --- Source PDS operations ---

// Repo is a repo entry from com.atproto.sync.listRepos.
type Repo struct {
	DID    string `json:"did"`
	Head   string `json:"head"`
	Rev    string `json:"rev"`
	Active bool   `json:"active"`
}

// listRepos enumerates all repos on the source PDS with pagination.
func (imp *Importer) listRepos() ([]Repo, error) {
	var all []Repo
	cursor := ""

	for {
		u := imp.source + "/xrpc/com.atproto.sync.listRepos?limit=1000"
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}

		resp, err := imp.client.Get(u)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("listRepos: %s - %s", resp.Status, string(body))
		}

		var result struct {
			Repos  []Repo `json:"repos"`
			Cursor string `json:"cursor"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return nil, err
		}

		all = append(all, result.Repos...)
		cursor = result.Cursor
		if cursor == "" || len(result.Repos) == 0 {
			break
		}
	}

	return all, nil
}

// RepoDescription is the response from com.atproto.repo.describeRepo.
type RepoDescription struct {
	Handle      string   `json:"handle"`
	DID         string   `json:"did"`
	Collections []string `json:"collections"`
}

// describeRepo gets the handle and collections for a repo.
func (imp *Importer) describeRepo(did string) (*RepoDescription, error) {
	u := imp.source + "/xrpc/com.atproto.repo.describeRepo?repo=" + url.QueryEscape(did)
	resp, err := imp.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("describeRepo: %s - %s", resp.Status, string(body))
	}

	var desc RepoDescription
	if err := json.NewDecoder(resp.Body).Decode(&desc); err != nil {
		return nil, err
	}
	return &desc, nil
}

// Record is a record entry from com.atproto.repo.listRecords.
type Record struct {
	URI   string         `json:"uri"`
	CID   string         `json:"cid"`
	Value map[string]any `json:"value"`
}

// listRecords pages through all records for a repo+collection on the source.
func (imp *Importer) listRecords(did, collection string) ([]Record, error) {
	var all []Record
	cursor := ""

	for {
		u := fmt.Sprintf("%s/xrpc/com.atproto.repo.listRecords?repo=%s&collection=%s&limit=100",
			imp.source, url.QueryEscape(did), url.QueryEscape(collection))
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}

		resp, err := imp.client.Get(u)
		if err != nil {
			return nil, err
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("listRecords: %s - %s", resp.Status, string(body))
		}

		var result struct {
			Records []Record `json:"records"`
			Cursor  string   `json:"cursor"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, err
		}

		all = append(all, result.Records...)

		if imp.maxRecords > 0 && len(all) >= imp.maxRecords {
			all = all[:imp.maxRecords]
			break
		}

		cursor = result.Cursor
		if cursor == "" || len(result.Records) == 0 {
			break
		}
	}

	return all, nil
}

// --- Target PDS operations ---

// addDomain creates the domain on the target primal-pds.
func (imp *Importer) addDomain() error {
	payload := map[string]string{"domain": imp.domain}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", imp.target+"/xrpc/host.primal.pds.addDomain", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+imp.adminKey)

	resp, err := imp.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s - %s", resp.Status, string(respBody))
	}

	return nil
}

// createAccount creates an account on the target primal-pds.
func (imp *Importer) createAccount(handle string) error {
	// Extract the handle prefix (everything before the domain suffix).
	handlePrefix := handle
	suffix := "." + imp.domain
	if strings.HasSuffix(handle, suffix) {
		handlePrefix = strings.TrimSuffix(handle, suffix)
	}

	payload := map[string]string{
		"domain":   imp.domain,
		"handle":   handlePrefix,
		"password": "imported-account-" + handlePrefix,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", imp.target+"/xrpc/host.primal.pds.createAccount", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+imp.adminKey)

	resp, err := imp.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s - %s", resp.Status, string(respBody))
	}

	return nil
}

// createRecord creates a single record on the target primal-pds.
func (imp *Importer) createRecord(handle, collection, rkey string, record map[string]any) error {
	payload := map[string]any{
		"repo":       handle,
		"collection": collection,
		"rkey":       rkey,
		"record":     record,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", imp.target+"/xrpc/com.atproto.repo.createRecord", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+imp.adminKey)

	resp, err := imp.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s - %s", resp.Status, string(respBody))
	}

	return nil
}

// copyRecords copies all records for one collection from source to target.
// Returns (copied, skipped, error).
func (imp *Importer) copyRecords(sourceDID, targetHandle, collection string) (int, int, error) {
	records, err := imp.listRecords(sourceDID, collection)
	if err != nil {
		return 0, 0, err
	}

	copied := 0
	skipped := 0

	for _, rec := range records {
		// Extract rkey from AT URI: at://did/collection/rkey
		rkey := extractRkey(rec.URI)
		if rkey == "" {
			log.Printf("    Warning: cannot extract rkey from URI %s", rec.URI)
			skipped++
			continue
		}

		if err := imp.createRecord(targetHandle, collection, rkey, rec.Value); err != nil {
			// Record might already exist from a previous import run.
			if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "InternalError") {
				skipped++
				continue
			}
			return copied, skipped, fmt.Errorf("create %s/%s: %w", collection, rkey, err)
		}
		copied++
	}

	return copied, skipped, nil
}

// --- Dry-run ---

// dryRunRepo counts records per collection for a repo without importing.
func (imp *Importer) dryRunRepo(desc *RepoDescription) {
	for _, coll := range desc.Collections {
		records, err := imp.listRecords(desc.DID, coll)
		if err != nil {
			log.Printf("  %s: error listing: %v", coll, err)
			continue
		}
		log.Printf("  %s: %d records", coll, len(records))
		imp.stats.RecordsCopied += len(records) // reuse for counting in dry-run
	}
}

// --- Helpers ---

// extractRkey gets the rkey from an AT URI (at://did/collection/rkey).
func extractRkey(uri string) string {
	parts := strings.Split(uri, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}
