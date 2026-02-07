package server

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/primal-host/primal-pds/internal/account"
	"github.com/primal-host/primal-pds/internal/domain"
)

// registerRoutes sets up all HTTP routes.
func (s *Server) registerRoutes() {
	// --- Public endpoints (no auth) ---
	s.echo.GET("/xrpc/_health", s.handleHealth)
	s.echo.GET("/.well-known/atproto-did", s.handleAtprotoDID)

	// --- Management API (admin auth required) ---
	admin := s.echo.Group("", s.adminAuth)

	// Domain management
	admin.POST("/xrpc/host.primal.pds.addDomain", s.handleAddDomain)
	admin.GET("/xrpc/host.primal.pds.listDomains", s.handleListDomains)
	admin.POST("/xrpc/host.primal.pds.updateDomain", s.handleUpdateDomain)
	admin.POST("/xrpc/host.primal.pds.removeDomain", s.handleRemoveDomain)

	// Account management
	admin.POST("/xrpc/host.primal.pds.createAccount", s.handleCreateAccount)
	admin.GET("/xrpc/host.primal.pds.listAccounts", s.handleListAccounts)
	admin.GET("/xrpc/host.primal.pds.getAccount", s.handleGetAccount)
	admin.POST("/xrpc/host.primal.pds.updateAccount", s.handleUpdateAccount)
	admin.POST("/xrpc/host.primal.pds.deleteAccount", s.handleDeleteAccount)

	// AT Protocol repo operations
	admin.POST("/xrpc/com.atproto.repo.createRecord", s.handleCreateRecord)
	admin.GET("/xrpc/com.atproto.repo.getRecord", s.handleGetRecord)
	admin.POST("/xrpc/com.atproto.repo.deleteRecord", s.handleDeleteRecord)
	admin.POST("/xrpc/com.atproto.repo.putRecord", s.handlePutRecord)
	admin.GET("/xrpc/com.atproto.repo.listRecords", s.handleListRecords)
	admin.GET("/xrpc/com.atproto.repo.describeRepo", s.handleDescribeRepo)
}

// =====================================================================
// Public endpoints
// =====================================================================

// handleHealth returns basic server health information.
func (s *Server) handleHealth(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"version": "0.3.0",
	})
}

// handleAtprotoDID resolves a DID for the handle implied by the Host
// header. The Host header (e.g., "alice.1440.news") is looked up in the
// accounts table to find the corresponding DID.
func (s *Server) handleAtprotoDID(c echo.Context) error {
	handle := stripPort(c.Request().Host)

	did, err := s.accounts.ResolveHandle(c.Request().Context(), handle)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "AccountNotFound",
				"message": "No account found for handle: " + handle,
			})
		}
		log.Printf("Error resolving handle %q: %v", handle, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to resolve handle",
		})
	}

	return c.String(http.StatusOK, did)
}

// =====================================================================
// Domain management
// =====================================================================

type addDomainRequest struct {
	Domain string `json:"domain"`
}

// addDomainResponse includes the domain and its auto-created owner account.
type addDomainResponse struct {
	Domain        *domain.Domain   `json:"domain"`
	AdminAccount  *account.Account `json:"adminAccount"`
	AdminPassword string           `json:"adminPassword"`
}

// handleAddDomain creates a new hosted domain, auto-creates the domain
// admin (owner) account, and regenerates the Traefik routing config.
// The response includes the auto-generated admin password — this is the
// only time it's returned in plaintext.
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

	ctx := c.Request().Context()

	// Create the domain.
	d, err := s.domains.Add(ctx, req.Domain)
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

	// Auto-create the domain admin (owner) account.
	// The handle is the bare domain name (e.g., "1440.news").
	adminPass, err := account.GeneratePassword()
	if err != nil {
		log.Printf("Error generating admin password for %q: %v", req.Domain, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to generate admin password",
		})
	}

	adminAcct, err := s.accounts.Create(ctx, account.CreateParams{
		Handle:   req.Domain,
		Password: adminPass,
		DomainID: d.ID,
		Role:     account.RoleOwner,
	})
	if err != nil {
		// Domain was created but admin account failed. Log but don't
		// roll back the domain — it can be retried.
		log.Printf("Error creating admin account for domain %q: %v", req.Domain, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Domain created but admin account creation failed",
		})
	}

	s.refreshTraefik(c)
	log.Printf("Domain added: %s (admin: %s, did: %s)", req.Domain, adminAcct.Handle, adminAcct.DID)

	return c.JSON(http.StatusOK, addDomainResponse{
		Domain:        d,
		AdminAccount:  adminAcct,
		AdminPassword: adminPass,
	})
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

// handleUpdateDomain changes a domain's status and regenerates Traefik config.
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

// handleRemoveDomain deletes a domain (and all its accounts via CASCADE)
// and regenerates the Traefik routing configuration.
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
	log.Printf("Domain removed: %s (all accounts cascade-deleted)", req.Domain)
	return c.JSON(http.StatusOK, map[string]string{
		"message": "Domain removed: " + req.Domain,
	})
}

// =====================================================================
// Account management
// =====================================================================

type createAccountRequest struct {
	Domain   string `json:"domain"`
	Handle   string `json:"handle"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// handleCreateAccount creates a new account under a domain. The handle
// is automatically suffixed with the domain if not already (e.g.,
// "alice" under "1440.news" becomes "alice.1440.news"). If password is
// omitted, one is auto-generated and returned.
func (s *Server) handleCreateAccount(c echo.Context) error {
	var req createAccountRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "Invalid JSON body",
		})
	}

	req.Domain = strings.TrimSpace(strings.ToLower(req.Domain))
	req.Handle = strings.TrimSpace(strings.ToLower(req.Handle))

	if req.Domain == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "domain is required",
		})
	}
	if req.Handle == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "handle is required",
		})
	}

	// Validate role if provided.
	switch req.Role {
	case "", account.RoleUser, account.RoleAdmin:
		// Valid (empty defaults to user in the store).
	case account.RoleOwner:
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "owner role is assigned automatically during domain creation",
		})
	default:
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "role must be 'user' or 'admin'",
		})
	}

	ctx := c.Request().Context()

	// Look up the domain.
	d, err := s.domains.GetByName(ctx, req.Domain)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "DomainNotFound",
				"message": "Domain not found: " + req.Domain,
			})
		}
		log.Printf("Error looking up domain %q: %v", req.Domain, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to look up domain",
		})
	}

	// Build the full handle: "alice" + "1440.news" → "alice.1440.news".
	// If the handle already ends with the domain, use it as-is.
	fullHandle := req.Handle
	if !strings.HasSuffix(fullHandle, "."+req.Domain) {
		fullHandle = req.Handle + "." + req.Domain
	}

	// Auto-generate password if not provided.
	password := req.Password
	autoGenerated := false
	if password == "" {
		password, err = account.GeneratePassword()
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error":   "InternalError",
				"message": "Failed to generate password",
			})
		}
		autoGenerated = true
	}

	acct, err := s.accounts.Create(ctx, account.CreateParams{
		Handle:   fullHandle,
		Email:    req.Email,
		Password: password,
		DomainID: d.ID,
		Role:     req.Role,
	})
	if err != nil {
		if isDuplicateKey(err) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error":   "HandleTaken",
				"message": "Handle already taken: " + fullHandle,
			})
		}
		log.Printf("Error creating account %q: %v", fullHandle, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to create account",
		})
	}

	log.Printf("Account created: %s (did: %s, role: %s, domain: %s)", acct.Handle, acct.DID, acct.Role, req.Domain)

	resp := map[string]any{"account": acct}
	if autoGenerated {
		resp["password"] = password
	}
	return c.JSON(http.StatusOK, resp)
}

// handleListAccounts returns accounts, optionally filtered by domain.
// Query parameter: ?domain=1440.news
func (s *Server) handleListAccounts(c echo.Context) error {
	ctx := c.Request().Context()
	domainID := 0

	if domainName := c.QueryParam("domain"); domainName != "" {
		d, err := s.domains.GetByName(ctx, domainName)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return c.JSON(http.StatusNotFound, map[string]string{
					"error":   "DomainNotFound",
					"message": "Domain not found: " + domainName,
				})
			}
			log.Printf("Error looking up domain %q: %v", domainName, err)
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error":   "InternalError",
				"message": "Failed to look up domain",
			})
		}
		domainID = d.ID
	}

	accounts, err := s.accounts.List(ctx, domainID)
	if err != nil {
		log.Printf("Error listing accounts: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to list accounts",
		})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"accounts": accounts,
	})
}

// handleGetAccount retrieves an account by handle or DID.
// Query parameters: ?handle=alice.1440.news or ?did=did:plc:...
func (s *Server) handleGetAccount(c echo.Context) error {
	ctx := c.Request().Context()
	handle := c.QueryParam("handle")
	did := c.QueryParam("did")

	if handle == "" && did == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "handle or did query parameter is required",
		})
	}

	var acct *account.Account
	var err error
	if handle != "" {
		acct, err = s.accounts.GetByHandle(ctx, handle)
	} else {
		acct, err = s.accounts.GetByDID(ctx, did)
	}

	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "AccountNotFound",
				"message": "Account not found",
			})
		}
		log.Printf("Error getting account: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to get account",
		})
	}
	return c.JSON(http.StatusOK, acct)
}

type updateAccountRequest struct {
	Handle string `json:"handle"`
	Status string `json:"status"`
	Role   string `json:"role"`
}

// handleUpdateAccount modifies an account's status and/or role.
// At least one of status or role must be provided.
func (s *Server) handleUpdateAccount(c echo.Context) error {
	var req updateAccountRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "Invalid JSON body",
		})
	}

	req.Handle = strings.TrimSpace(strings.ToLower(req.Handle))
	if req.Handle == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "handle is required",
		})
	}

	if req.Status == "" && req.Role == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "at least one of status or role is required",
		})
	}

	ctx := c.Request().Context()
	var result *account.Account
	var err error

	// Update status if provided.
	if req.Status != "" {
		switch req.Status {
		case account.StatusActive, account.StatusSuspended, account.StatusDisabled, account.StatusRemoved:
		default:
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error":   "InvalidRequest",
				"message": "status must be 'active', 'suspended', 'disabled', or 'removed'",
			})
		}

		result, err = s.accounts.UpdateStatus(ctx, req.Handle, req.Status)
		if err != nil {
			return accountError(c, err, req.Handle)
		}
	}

	// Update role if provided.
	if req.Role != "" {
		switch req.Role {
		case account.RoleAdmin, account.RoleUser:
		default:
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error":   "InvalidRequest",
				"message": "role must be 'admin' or 'user'",
			})
		}

		result, err = s.accounts.UpdateRole(ctx, req.Handle, req.Role)
		if err != nil {
			return accountError(c, err, req.Handle)
		}
	}

	log.Printf("Account updated: %s (status=%s, role=%s)", req.Handle, req.Status, req.Role)
	return c.JSON(http.StatusOK, result)
}

type deleteAccountRequest struct {
	Handle string `json:"handle"`
}

// handleDeleteAccount permanently removes an account. Owner accounts
// cannot be deleted — remove the domain instead.
func (s *Server) handleDeleteAccount(c echo.Context) error {
	var req deleteAccountRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "Invalid JSON body",
		})
	}

	req.Handle = strings.TrimSpace(strings.ToLower(req.Handle))
	if req.Handle == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "handle is required",
		})
	}

	if err := s.accounts.Delete(c.Request().Context(), req.Handle); err != nil {
		return accountError(c, err, req.Handle)
	}

	log.Printf("Account deleted: %s", req.Handle)
	return c.JSON(http.StatusOK, map[string]string{
		"message": "Account deleted: " + req.Handle,
	})
}

// =====================================================================
// Helpers
// =====================================================================

// refreshTraefik regenerates the Traefik dynamic config file.
func (s *Server) refreshTraefik(c echo.Context) {
	if err := s.domains.WriteTraefikConfig(c.Request().Context(), s.cfg.TraefikConfigDir); err != nil {
		log.Printf("Warning: failed to write Traefik config: %v", err)
	}
}

// accountError maps account package errors to HTTP responses.
func accountError(c echo.Context, err error, handle string) error {
	switch {
	case errors.Is(err, account.ErrNotFound):
		return c.JSON(http.StatusNotFound, map[string]string{
			"error":   "AccountNotFound",
			"message": "Account not found: " + handle,
		})
	case errors.Is(err, account.ErrOwnerProtected):
		return c.JSON(http.StatusForbidden, map[string]string{
			"error":   "OwnerProtected",
			"message": err.Error(),
		})
	default:
		log.Printf("Error on account %q: %v", handle, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to update account",
		})
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
