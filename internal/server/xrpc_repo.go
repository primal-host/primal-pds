package server

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/primal-host/primal-pds/internal/account"
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

	ctx := c.Request().Context()
	var uri, cidStr, rev string

	if req.RKey != "" {
		uri, cidStr, rev, err = s.repos.PutRecord(ctx, pool, acct.DID, acct.SigningKey, req.Collection, req.RKey, req.Record)
	} else {
		uri, cidStr, rev, err = s.repos.CreateRecord(ctx, pool, acct.DID, acct.SigningKey, req.Collection, req.Record)
	}
	if err != nil {
		log.Printf("Error creating record for %s: %v", acct.DID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to create record",
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"uri": uri,
		"cid": cidStr,
		"commit": map[string]string{
			"cid": cidStr,
			"rev": rev,
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

	rev, err := s.repos.DeleteRecord(c.Request().Context(), pool, acct.DID, acct.SigningKey, req.Collection, req.RKey)
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

	// Get updated commit CID.
	commitCID, _, err := s.repos.GetRoot(c.Request().Context(), pool, acct.DID)
	if err != nil {
		commitCID = ""
	}

	return c.JSON(http.StatusOK, map[string]any{
		"commit": map[string]string{
			"cid": commitCID,
			"rev": rev,
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

	uri, cidStr, rev, err := s.repos.PutRecord(c.Request().Context(), pool, acct.DID, acct.SigningKey, req.Collection, req.RKey, req.Record)
	if err != nil {
		log.Printf("Error putting record %s/%s for %s: %v", req.Collection, req.RKey, acct.DID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to put record",
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"uri": uri,
		"cid": cidStr,
		"commit": map[string]string{
			"cid": cidStr,
			"rev": rev,
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

	return c.JSON(http.StatusOK, map[string]any{
		"handle":          acct.Handle,
		"did":             acct.DID,
		"didDoc":          map[string]any{},
		"collections":     collections,
		"handleIsCorrect": true,
	})
}
