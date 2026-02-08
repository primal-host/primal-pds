package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/primal-host/primal-pds/internal/account"
	"github.com/primal-host/primal-pds/internal/events"
	"github.com/primal-host/primal-pds/internal/repo"

	"github.com/ipfs/go-cid"
)

// resolveRepo resolves a "repo" parameter (handle or DID) to an Account
// and the tenant pool where that account lives.
func (s *Server) resolveRepo(c echo.Context, repoID string) (*account.Account, *pgxpool.Pool, error) {
	ctx := c.Request().Context()

	var domainName string
	var err error

	if strings.HasPrefix(repoID, "did:") {
		// Look up domain from DID routing table.
		domainName, err = s.mgmtDB.LookupDIDDomain(ctx, repoID)
		if err != nil {
			return nil, nil, account.ErrNotFound
		}
	} else {
		// Extract domain from handle suffix.
		domainName = extractDomainFromHandle(repoID, s.pools)
		if domainName == "" {
			return nil, nil, account.ErrNotFound
		}
	}

	pool := s.pools.Get(domainName)
	if pool == nil {
		return nil, nil, account.ErrNotFound
	}

	accounts := s.tenantStore(pool)
	var acct *account.Account
	if strings.HasPrefix(repoID, "did:") {
		acct, err = accounts.GetByDID(ctx, repoID)
	} else {
		acct, err = accounts.GetByHandle(ctx, repoID)
	}
	if err != nil {
		return nil, nil, err
	}

	return acct, pool, nil
}

// repoNotFound returns a standard error response for missing repos.
func repoNotFound(c echo.Context, repoID string) error {
	return c.JSON(http.StatusNotFound, map[string]string{
		"error":   "RepoNotFound",
		"message": "Repository not found: " + repoID,
	})
}

// --- createRecord ---

type createRecordRequest struct {
	Repo       string         `json:"repo"`
	Collection string         `json:"collection"`
	RKey       string         `json:"rkey"`
	Record     map[string]any `json:"record"`
}

func (s *Server) handleCreateRecord(c echo.Context) error {
	var req createRecordRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "Invalid JSON body",
		})
	}

	if req.Repo == "" || req.Collection == "" || req.Record == nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "repo, collection, and record are required",
		})
	}

	acct, pool, err := s.resolveRepo(c, req.Repo)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return repoNotFound(c, req.Repo)
		}
		log.Printf("Error resolving repo %q: %v", req.Repo, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to resolve repo",
		})
	}

	if err := checkRepoAuth(c, acct.DID); err != nil {
		return err
	}

	ctx := c.Request().Context()
	var uri string
	var result *repo.CommitResult

	if req.RKey != "" {
		uri, result, err = s.repos.PutRecord(ctx, pool, acct.DID, acct.SigningKey, req.Collection, req.RKey, req.Record)
	} else {
		uri, result, err = s.repos.CreateRecord(ctx, pool, acct.DID, acct.SigningKey, req.Collection, req.Record)
	}
	if err != nil {
		log.Printf("Error creating record for %s: %v", acct.DID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to create record",
		})
	}

	s.emitCommitEvent(ctx, acct.DID, result)

	return c.JSON(http.StatusOK, map[string]any{
		"uri": uri,
		"cid": result.CommitCID,
		"commit": map[string]string{
			"cid": result.CommitCID,
			"rev": result.Rev,
		},
	})
}

// --- getRecord ---

func (s *Server) handleGetRecord(c echo.Context) error {
	repoID := c.QueryParam("repo")
	collection := c.QueryParam("collection")
	rkey := c.QueryParam("rkey")

	if repoID == "" || collection == "" || rkey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "repo, collection, and rkey query parameters are required",
		})
	}

	acct, pool, err := s.resolveRepo(c, repoID)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return repoNotFound(c, repoID)
		}
		log.Printf("Error resolving repo %q: %v", repoID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to resolve repo",
		})
	}

	cidStr, record, err := s.repos.GetRecord(c.Request().Context(), pool, acct.DID, collection, rkey)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "RecordNotFound",
				"message": "Record not found",
			})
		}
		log.Printf("Error getting record %s/%s for %s: %v", collection, rkey, acct.DID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to get record",
		})
	}

	uri := "at://" + acct.DID + "/" + collection + "/" + rkey
	return c.JSON(http.StatusOK, map[string]any{
		"uri":   uri,
		"cid":   cidStr,
		"value": record,
	})
}

// --- deleteRecord ---

type deleteRecordRequest struct {
	Repo       string `json:"repo"`
	Collection string `json:"collection"`
	RKey       string `json:"rkey"`
}

func (s *Server) handleDeleteRecord(c echo.Context) error {
	var req deleteRecordRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "Invalid JSON body",
		})
	}

	if req.Repo == "" || req.Collection == "" || req.RKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "repo, collection, and rkey are required",
		})
	}

	acct, pool, err := s.resolveRepo(c, req.Repo)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return repoNotFound(c, req.Repo)
		}
		log.Printf("Error resolving repo %q: %v", req.Repo, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to resolve repo",
		})
	}

	if err := checkRepoAuth(c, acct.DID); err != nil {
		return err
	}

	result, err := s.repos.DeleteRecord(c.Request().Context(), pool, acct.DID, acct.SigningKey, req.Collection, req.RKey)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "RecordNotFound",
				"message": "Record not found",
			})
		}
		log.Printf("Error deleting record %s/%s for %s: %v", req.Collection, req.RKey, acct.DID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to delete record",
		})
	}

	s.emitCommitEvent(c.Request().Context(), acct.DID, result)

	return c.JSON(http.StatusOK, map[string]any{
		"commit": map[string]string{
			"cid": result.CommitCID,
			"rev": result.Rev,
		},
	})
}

// --- putRecord ---

type putRecordRequest struct {
	Repo       string         `json:"repo"`
	Collection string         `json:"collection"`
	RKey       string         `json:"rkey"`
	Record     map[string]any `json:"record"`
}

func (s *Server) handlePutRecord(c echo.Context) error {
	var req putRecordRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "Invalid JSON body",
		})
	}

	if req.Repo == "" || req.Collection == "" || req.RKey == "" || req.Record == nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "repo, collection, rkey, and record are required",
		})
	}

	acct, pool, err := s.resolveRepo(c, req.Repo)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return repoNotFound(c, req.Repo)
		}
		log.Printf("Error resolving repo %q: %v", req.Repo, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to resolve repo",
		})
	}

	if err := checkRepoAuth(c, acct.DID); err != nil {
		return err
	}

	uri, result, err := s.repos.PutRecord(c.Request().Context(), pool, acct.DID, acct.SigningKey, req.Collection, req.RKey, req.Record)
	if err != nil {
		log.Printf("Error putting record %s/%s for %s: %v", req.Collection, req.RKey, acct.DID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to put record",
		})
	}

	s.emitCommitEvent(c.Request().Context(), acct.DID, result)

	return c.JSON(http.StatusOK, map[string]any{
		"uri": uri,
		"cid": result.CommitCID,
		"commit": map[string]string{
			"cid": result.CommitCID,
			"rev": result.Rev,
		},
	})
}

// --- listRecords ---

func (s *Server) handleListRecords(c echo.Context) error {
	repoID := c.QueryParam("repo")
	collection := c.QueryParam("collection")

	if repoID == "" || collection == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "repo and collection query parameters are required",
		})
	}

	limit := 50
	if l := c.QueryParam("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	cursor := c.QueryParam("cursor")
	reverse := c.QueryParam("reverse") == "true"

	acct, pool, err := s.resolveRepo(c, repoID)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return repoNotFound(c, repoID)
		}
		log.Printf("Error resolving repo %q: %v", repoID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to resolve repo",
		})
	}

	records, nextCursor, err := s.repos.ListRecords(c.Request().Context(), pool, acct.DID, collection, limit, cursor, reverse)
	if err != nil {
		log.Printf("Error listing records for %s/%s: %v", acct.DID, collection, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to list records",
		})
	}

	resp := map[string]any{
		"records": records,
	}
	if nextCursor != "" {
		resp["cursor"] = nextCursor
	}
	return c.JSON(http.StatusOK, resp)
}

// --- describeRepo ---

func (s *Server) handleDescribeRepo(c echo.Context) error {
	repoID := c.QueryParam("repo")
	if repoID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "repo query parameter is required",
		})
	}

	acct, pool, err := s.resolveRepo(c, repoID)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return repoNotFound(c, repoID)
		}
		log.Printf("Error resolving repo %q: %v", repoID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to resolve repo",
		})
	}

	collections, err := s.repos.DescribeRepo(c.Request().Context(), pool, acct.DID)
	if err != nil {
		log.Printf("Error describing repo for %s: %v", acct.DID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to describe repo",
		})
	}

	// Extract domain from handle to build DID document.
	domainName := extractDomainFromHandle(acct.Handle, s.pools)
	didDoc := map[string]any{}
	if domainName != "" && acct.SigningKey != "" {
		doc, err := account.BuildDIDDocument(acct.DID, acct.Handle, acct.SigningKey, domainName)
		if err == nil {
			didDoc = map[string]any{
				"@context":           doc.Context,
				"id":                 doc.ID,
				"alsoKnownAs":       doc.AlsoKnownAs,
				"verificationMethod": doc.VerificationMethod,
				"service":            doc.Service,
			}
		} else {
			log.Printf("Warning: failed to build DID doc for %s: %v", acct.DID, err)
		}
	}

	return c.JSON(http.StatusOK, map[string]any{
		"handle":          acct.Handle,
		"did":             acct.DID,
		"didDoc":          didDoc,
		"collections":     collections,
		"handleIsCorrect": true,
	})
}

// checkRepoAuth verifies that the authenticated caller is allowed to
// modify the given repo. Admins can modify any repo; JWT users can only
// modify their own.
func checkRepoAuth(c echo.Context, repoDID string) error {
	ac := getAuth(c)
	if ac == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error":   "AuthRequired",
			"message": "Authentication required",
		})
	}
	if ac.IsAdmin {
		return nil
	}
	if ac.DID != repoDID {
		return c.JSON(http.StatusForbidden, map[string]string{
			"error":   "Forbidden",
			"message": "Cannot modify another account's repository",
		})
	}
	return nil
}

// emitCommitEvent converts a CommitResult to a CommitInfo and emits it
// through the EventManager. Errors are logged but not returned — event
// emission is best-effort and must not break the mutation path.
func (s *Server) emitCommitEvent(ctx context.Context, did string, result *repo.CommitResult) {
	if s.events == nil || result == nil {
		return
	}

	commitCID, err := cid.Decode(result.CommitCID)
	if err != nil {
		log.Printf("Warning: emit event: decode commit cid: %v", err)
		return
	}

	ops := make([]events.OpInfo, len(result.Ops))
	for i, op := range result.Ops {
		ops[i] = events.OpInfo{
			Action: op.Action,
			Path:   op.Path,
			CID:    op.CID,
			Prev:   op.Prev,
		}
	}

	info := &events.CommitInfo{
		DID:       did,
		Rev:       result.Rev,
		PrevRev:   result.PrevRev,
		CommitCID: commitCID.String(),
		PrevData:  result.PrevData,
		DiffCAR:   result.DiffCAR,
		Ops:       ops,
		Time:      time.Now(),
	}

	if err := s.events.Emit(ctx, info); err != nil {
		log.Printf("Warning: emit event for %s: %v", did, err)
	}
}
