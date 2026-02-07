package server

import (
	"errors"
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/primal-host/primal-pds/internal/account"
)

// handleGetRepo streams the full repository as a CAR v1 archive.
// GET /xrpc/com.atproto.sync.getRepo?did=...
func (s *Server) handleGetRepo(c echo.Context) error {
	did := c.QueryParam("did")
	if did == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "did query parameter is required",
		})
	}

	_, pool, err := s.resolveRepo(c, did)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "RepoNotFound",
				"message": "Repository not found: " + did,
			})
		}
		log.Printf("Error resolving repo %q: %v", did, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to resolve repo",
		})
	}

	ctx := c.Request().Context()
	c.Response().Header().Set("Content-Type", "application/vnd.ipld.car")
	c.Response().WriteHeader(http.StatusOK)

	if err := s.repos.ExportRepo(ctx, pool, did, c.Response().Writer); err != nil {
		log.Printf("Error exporting repo %s: %v", did, err)
		// Headers already sent — can't return JSON error.
		return nil
	}
	return nil
}

// handleGetLatestCommit returns the current commit CID and rev.
// GET /xrpc/com.atproto.sync.getLatestCommit?did=...
func (s *Server) handleGetLatestCommit(c echo.Context) error {
	did := c.QueryParam("did")
	if did == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "did query parameter is required",
		})
	}

	acct, pool, err := s.resolveRepo(c, did)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "RepoNotFound",
				"message": "Repository not found: " + did,
			})
		}
		log.Printf("Error resolving repo %q: %v", did, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to resolve repo",
		})
	}

	commitCID, rev, err := s.repos.GetRoot(c.Request().Context(), pool, acct.DID)
	if err != nil {
		log.Printf("Error getting root for %s: %v", did, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to get latest commit",
		})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"cid": commitCID,
		"rev": rev,
	})
}
