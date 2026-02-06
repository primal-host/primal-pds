package server

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/primal-host/primal-pds/internal/domain"
)

// registerRoutes sets up all HTTP routes.
func (s *Server) registerRoutes() {
	// --- Public endpoints (no auth) ---
	s.echo.GET("/xrpc/_health", s.handleHealth)
	s.echo.GET("/.well-known/atproto-did", s.handleAtprotoDID)

	// --- Management API (admin auth required) ---
	admin := s.echo.Group("", s.adminAuth)
	admin.POST("/xrpc/host.primal.pds.addDomain", s.handleAddDomain)
	admin.GET("/xrpc/host.primal.pds.listDomains", s.handleListDomains)
	admin.POST("/xrpc/host.primal.pds.updateDomain", s.handleUpdateDomain)
	admin.POST("/xrpc/host.primal.pds.removeDomain", s.handleRemoveDomain)
}

// handleHealth returns basic server health information.
// Used by AT Protocol tooling and monitoring to verify the PDS is alive.
func (s *Server) handleHealth(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"version": "0.1.0",
	})
}

// handleAtprotoDID resolves a DID for the handle implied by the Host
// header. Phase 1 always returns 404 since accounts are not yet
// implemented. Phase 2 will look up the DID in the accounts table.
func (s *Server) handleAtprotoDID(c echo.Context) error {
	host := stripPort(c.Request().Host)

	return c.JSON(http.StatusNotFound, map[string]string{
		"error":   "AccountNotFound",
		"message": "No account found for host: " + host,
	})
}

// --- Management API handlers ---

type addDomainRequest struct {
	Domain string `json:"domain"`
}

// handleAddDomain creates a new hosted domain and regenerates the
// Traefik routing configuration.
func (s *Server) handleAddDomain(c echo.Context) error {
	var req addDomainRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "Invalid JSON body",
		})
	}

	req.Domain = strings.TrimSpace(strings.ToLower(req.Domain))
	if req.Domain == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "domain is required",
		})
	}

	d, err := s.domains.Add(c.Request().Context(), req.Domain)
	if err != nil {
		if isDuplicateKey(err) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error":   "DomainExists",
				"message": "Domain already exists: " + req.Domain,
			})
		}
		log.Printf("Error adding domain %q: %v", req.Domain, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to add domain",
		})
	}

	s.refreshTraefik(c)
	log.Printf("Domain added: %s", req.Domain)
	return c.JSON(http.StatusOK, d)
}

// handleListDomains returns all configured domains.
func (s *Server) handleListDomains(c echo.Context) error {
	domains, err := s.domains.List(c.Request().Context())
	if err != nil {
		log.Printf("Error listing domains: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to list domains",
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"domains": domains,
	})
}

type updateDomainRequest struct {
	Domain string `json:"domain"`
	Status string `json:"status"`
}

// handleUpdateDomain changes a domain's status (active or disabled)
// and regenerates the Traefik routing configuration.
func (s *Server) handleUpdateDomain(c echo.Context) error {
	var req updateDomainRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "Invalid JSON body",
		})
	}

	req.Domain = strings.TrimSpace(strings.ToLower(req.Domain))
	if req.Domain == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "domain is required",
		})
	}

	switch req.Status {
	case "active", "disabled":
	default:
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "status must be 'active' or 'disabled'",
		})
	}

	d, err := s.domains.Update(c.Request().Context(), req.Domain, req.Status)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "DomainNotFound",
				"message": "Domain not found: " + req.Domain,
			})
		}
		log.Printf("Error updating domain %q: %v", req.Domain, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to update domain",
		})
	}

	s.refreshTraefik(c)
	log.Printf("Domain updated: %s -> %s", req.Domain, req.Status)
	return c.JSON(http.StatusOK, d)
}

type removeDomainRequest struct {
	Domain string `json:"domain"`
}

// handleRemoveDomain deletes a domain and regenerates the Traefik
// routing configuration.
func (s *Server) handleRemoveDomain(c echo.Context) error {
	var req removeDomainRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "Invalid JSON body",
		})
	}

	req.Domain = strings.TrimSpace(strings.ToLower(req.Domain))
	if req.Domain == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "domain is required",
		})
	}

	if err := s.domains.Remove(c.Request().Context(), req.Domain); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "DomainNotFound",
				"message": "Domain not found: " + req.Domain,
			})
		}
		log.Printf("Error removing domain %q: %v", req.Domain, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to remove domain",
		})
	}

	s.refreshTraefik(c)
	log.Printf("Domain removed: %s", req.Domain)
	return c.JSON(http.StatusOK, map[string]string{
		"message": "Domain removed: " + req.Domain,
	})
}

// --- Helpers ---

// refreshTraefik regenerates the Traefik dynamic config file. Errors
// are logged but not returned to the caller — the primary operation
// (domain change) has already succeeded.
func (s *Server) refreshTraefik(c echo.Context) {
	if err := s.domains.WriteTraefikConfig(c.Request().Context(), s.cfg.TraefikConfigDir); err != nil {
		log.Printf("Warning: failed to write Traefik config: %v", err)
	}
}

// stripPort removes the port suffix from a host string.
func stripPort(host string) string {
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		return host[:idx]
	}
	return host
}

// isDuplicateKey checks whether an error is a PostgreSQL unique
// constraint violation (error code 23505).
func isDuplicateKey(err error) bool {
	return strings.Contains(err.Error(), "23505") ||
		strings.Contains(err.Error(), "duplicate key") ||
		strings.Contains(err.Error(), "unique constraint")
}
