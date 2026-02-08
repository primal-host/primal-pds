package server

import (
	"errors"
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/primal-host/primal-pds/internal/account"
)

// handleResolveHandle resolves a handle to a DID.
// GET /xrpc/com.atproto.identity.resolveHandle?handle=...
func (s *Server) handleResolveHandle(c echo.Context) error {
	handle := c.QueryParam("handle")
	if handle == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "handle query parameter is required",
		})
	}

	ctx := c.Request().Context()

	// Extract domain from handle.
	domainName := extractDomainFromHandle(handle, s.pools)
	if domainName == "" {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error":   "HandleNotFound",
			"message": "Unable to resolve handle: " + handle,
		})
	}

	pool := s.pools.Get(domainName)
	if pool == nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error":   "HandleNotFound",
			"message": "Unable to resolve handle: " + handle,
		})
	}

	did, err := s.tenantStore(pool).ResolveHandle(ctx, handle)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "HandleNotFound",
				"message": "Unable to resolve handle: " + handle,
			})
		}
		log.Printf("Error resolving handle %q: %v", handle, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to resolve handle",
		})
	}

	return c.JSON(http.StatusOK, map[string]string{
		"did": did,
	})
}
