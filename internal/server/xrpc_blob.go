package server

import (
	"errors"
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/primal-host/primal-pds/internal/account"
)

// handleUploadBlob handles media uploads and returns a blob reference.
// POST /xrpc/com.atproto.repo.uploadBlob
func (s *Server) handleUploadBlob(c echo.Context) error {
	ac := getAuth(c)
	if ac == nil || (ac.DID == "" && !ac.IsAdmin) {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error":   "AuthRequired",
			"message": "Authentication required",
		})
	}

	// Determine the DID for storage. Admin uploads require a repo param.
	did := ac.DID
	if did == "" && ac.IsAdmin {
		did = c.QueryParam("did")
		if did == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error":   "InvalidRequest",
				"message": "Admin uploads require a did query parameter",
			})
		}
	}

	ctx := c.Request().Context()

	// Resolve to get the pool.
	domainName, err := s.mgmtDB.LookupDIDDomain(ctx, did)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error":   "RepoNotFound",
			"message": "Account not found for DID: " + did,
		})
	}

	pool := s.pools.Get(domainName)
	if pool == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Domain unavailable",
		})
	}

	mimeType := c.Request().Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	ref, err := s.blobs.Upload(ctx, pool, did, mimeType, c.Request().Body)
	if err != nil {
		log.Printf("Error uploading blob for %s: %v", did, err)
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "BlobError",
			"message": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"blob": map[string]any{
			"$type":    "blob",
			"ref":      map[string]string{"$link": ref.CID},
			"mimeType": ref.MimeType,
			"size":     ref.Size,
		},
	})
}

// handleGetBlob retrieves a blob by DID and CID.
// GET /xrpc/com.atproto.sync.getBlob?did=...&cid=...
func (s *Server) handleGetBlob(c echo.Context) error {
	did := c.QueryParam("did")
	cidStr := c.QueryParam("cid")

	if did == "" || cidStr == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "did and cid query parameters are required",
		})
	}

	ctx := c.Request().Context()

	// Resolve to get the pool.
	domainName, err := s.mgmtDB.LookupDIDDomain(ctx, did)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "BlobNotFound",
				"message": "Blob not found",
			})
		}
		return c.JSON(http.StatusNotFound, map[string]string{
			"error":   "BlobNotFound",
			"message": "Blob not found",
		})
	}

	pool := s.pools.Get(domainName)
	if pool == nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error":   "BlobNotFound",
			"message": "Blob not found",
		})
	}

	data, mimeType, err := s.blobs.Get(ctx, pool, did, cidStr)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error":   "BlobNotFound",
			"message": "Blob not found",
		})
	}

	return c.Blob(http.StatusOK, mimeType, data)
}
