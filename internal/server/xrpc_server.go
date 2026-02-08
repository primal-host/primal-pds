package server

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/primal-host/primal-pds/internal/account"
)

// handleDescribeServer returns server metadata including the service DID
// and available user domains.
// GET /xrpc/com.atproto.server.describeServer
func (s *Server) handleDescribeServer(c echo.Context) error {
	// Derive did:web from serviceURL.
	serviceDID := ""
	if s.cfg.ServiceURL != "" {
		host := s.cfg.ServiceURL
		host = strings.TrimPrefix(host, "https://")
		host = strings.TrimPrefix(host, "http://")
		host = strings.TrimSuffix(host, "/")
		serviceDID = "did:web:" + host
	}

	// Collect active domain names.
	domains, err := s.domains.ListActive(c.Request().Context())
	if err != nil {
		log.Printf("Error listing domains for describeServer: %v", err)
	}
	availableDomains := make([]string, 0, len(domains))
	for _, d := range domains {
		availableDomains = append(availableDomains, "."+d.Domain)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"did":                  serviceDID,
		"availableUserDomains": availableDomains,
		"inviteCodeRequired":   false,
	})
}

// handleCreateSession authenticates a user by handle/DID + password and
// returns a JWT token pair.
// POST /xrpc/com.atproto.server.createSession
func (s *Server) handleCreateSession(c echo.Context) error {
	var req struct {
		Identifier string `json:"identifier"`
		Password   string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "Invalid JSON body",
		})
	}

	if req.Identifier == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "identifier and password are required",
		})
	}

	ctx := c.Request().Context()

	// Resolve identifier to handle. If it's a DID, look up the account first.
	var handle string
	var domainName string
	if strings.HasPrefix(req.Identifier, "did:") {
		// Look up domain from DID routing.
		dn, err := s.mgmtDB.LookupDIDDomain(ctx, req.Identifier)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error":   "AuthenticationRequired",
				"message": "Invalid identifier or password",
			})
		}
		domainName = dn
		pool := s.pools.Get(domainName)
		if pool == nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error":   "AuthenticationRequired",
				"message": "Invalid identifier or password",
			})
		}
		acct, err := s.tenantStore(pool).GetByDID(ctx, req.Identifier)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error":   "AuthenticationRequired",
				"message": "Invalid identifier or password",
			})
		}
		handle = acct.Handle
	} else {
		handle = strings.ToLower(strings.TrimSpace(req.Identifier))
		domainName = extractDomainFromHandle(handle, s.pools)
		if domainName == "" {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error":   "AuthenticationRequired",
				"message": "Invalid identifier or password",
			})
		}
	}

	pool := s.pools.Get(domainName)
	if pool == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error":   "AuthenticationRequired",
			"message": "Invalid identifier or password",
		})
	}

	acct, err := s.tenantStore(pool).VerifyPassword(ctx, handle, req.Password)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error":   "AuthenticationRequired",
			"message": "Invalid identifier or password",
		})
	}

	tokens, err := s.jwt.CreateTokenPair(acct.DID)
	if err != nil {
		log.Printf("Error creating tokens for %s: %v", acct.DID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to create session",
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"did":        acct.DID,
		"handle":     acct.Handle,
		"email":      acct.Email,
		"accessJwt":  tokens.AccessJwt,
		"refreshJwt": tokens.RefreshJwt,
	})
}

// handleRefreshSession issues a new token pair from a valid refresh token.
// POST /xrpc/com.atproto.server.refreshSession
func (s *Server) handleRefreshSession(c echo.Context) error {
	ac := getAuth(c)
	if ac == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error":   "AuthRequired",
			"message": "Refresh token required",
		})
	}

	// Look up the account to get current handle/email.
	ctx := c.Request().Context()
	domainName, err := s.mgmtDB.LookupDIDDomain(ctx, ac.DID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error":   "InvalidToken",
			"message": "Account not found",
		})
	}

	pool := s.pools.Get(domainName)
	if pool == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Account domain unavailable",
		})
	}

	acct, err := s.tenantStore(pool).GetByDID(ctx, ac.DID)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error":   "InvalidToken",
			"message": "Account not found",
		})
	}

	tokens, err := s.jwt.CreateTokenPair(ac.DID)
	if err != nil {
		log.Printf("Error refreshing tokens for %s: %v", ac.DID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to refresh session",
		})
	}

	return c.JSON(http.StatusOK, map[string]any{
		"did":        acct.DID,
		"handle":     acct.Handle,
		"accessJwt":  tokens.AccessJwt,
		"refreshJwt": tokens.RefreshJwt,
	})
}

// handleGetSession returns the current session info for a valid access token.
// GET /xrpc/com.atproto.server.getSession
func (s *Server) handleGetSession(c echo.Context) error {
	ac := getAuth(c)
	if ac == nil || (ac.DID == "" && !ac.IsAdmin) {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error":   "AuthRequired",
			"message": "Access token required",
		})
	}

	// Admin key sessions don't have a DID — return minimal info.
	if ac.IsAdmin && ac.DID == "" {
		return c.JSON(http.StatusOK, map[string]any{
			"did":    "",
			"handle": "admin",
		})
	}

	ctx := c.Request().Context()
	domainName, err := s.mgmtDB.LookupDIDDomain(ctx, ac.DID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error":   "AccountNotFound",
			"message": "Account not found for DID",
		})
	}

	pool := s.pools.Get(domainName)
	if pool == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Account domain unavailable",
		})
	}

	acct, err := s.tenantStore(pool).GetByDID(ctx, ac.DID)
	if err != nil {
		if errors.Is(err, account.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{
				"error":   "AccountNotFound",
				"message": "Account not found",
			})
		}
		log.Printf("Error getting session account %s: %v", ac.DID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to get session",
		})
	}

	resp := map[string]any{
		"did":    acct.DID,
		"handle": acct.Handle,
		"email":  acct.Email,
	}

	// Include DID document if possible.
	if acct.SigningKey != "" {
		doc, err := account.BuildDIDDocument(acct.DID, acct.Handle, acct.SigningKey, domainName)
		if err == nil {
			resp["didDoc"] = map[string]any{
				"@context":           doc.Context,
				"id":                 doc.ID,
				"alsoKnownAs":        doc.AlsoKnownAs,
				"verificationMethod": doc.VerificationMethod,
				"service":            doc.Service,
			}
		}
	}

	return c.JSON(http.StatusOK, resp)
}

// handleDeleteSession is a no-op for the stateless JWT MVP. Clients
// should discard tokens locally.
// POST /xrpc/com.atproto.server.deleteSession
func (s *Server) handleDeleteSession(c echo.Context) error {
	return c.NoContent(http.StatusOK)
}

// handleCreateAccountXRPC handles public account creation via the standard
// AT Protocol endpoint. Gated by registrationOpen config or admin key.
// POST /xrpc/com.atproto.server.createAccount
func (s *Server) handleCreateAccountXRPC(c echo.Context) error {
	ac := getAuth(c)

	// Gate: require admin key or open registration.
	if !s.cfg.RegistrationOpen {
		if ac == nil || !ac.IsAdmin {
			return c.JSON(http.StatusForbidden, map[string]string{
				"error":   "RegistrationClosed",
				"message": "Public registration is not available on this server",
			})
		}
	}

	var req struct {
		Handle   string `json:"handle"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "Invalid JSON body",
		})
	}

	req.Handle = strings.TrimSpace(strings.ToLower(req.Handle))
	if req.Handle == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidRequest",
			"message": "handle and password are required",
		})
	}

	// Extract domain from handle suffix.
	domainName := extractDomainFromHandle(req.Handle, s.pools)
	if domainName == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidHandle",
			"message": "Handle must end with a hosted domain suffix",
		})
	}

	ctx := c.Request().Context()
	pool := s.pools.Get(domainName)
	if pool == nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error":   "InvalidHandle",
			"message": "Domain not available",
		})
	}

	accounts := s.tenantStore(pool)
	acct, err := accounts.Create(ctx, account.CreateParams{
		Handle:          req.Handle,
		Email:           req.Email,
		Password:        req.Password,
		ServiceEndpoint: s.serviceEndpointForDomain(domainName),
	})
	if err != nil {
		if isDuplicateKey(err) {
			return c.JSON(http.StatusConflict, map[string]string{
				"error":   "HandleTaken",
				"message": "Handle already taken: " + req.Handle,
			})
		}
		log.Printf("Error creating account %q: %v", req.Handle, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Failed to create account",
		})
	}

	// Insert DID routing.
	if err := s.mgmtDB.InsertDIDRouting(ctx, acct.DID, domainName); err != nil {
		log.Printf("Error inserting DID routing for %q: %v", acct.DID, err)
	}

	// Init repo.
	if err := s.repos.InitRepo(ctx, pool, acct.DID, acct.SigningKey); err != nil {
		log.Printf("Warning: failed to init repo for %s: %v", acct.DID, err)
	}

	// Create tokens.
	tokens, err := s.jwt.CreateTokenPair(acct.DID)
	if err != nil {
		log.Printf("Error creating tokens for new account %s: %v", acct.DID, err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error":   "InternalError",
			"message": "Account created but failed to generate session tokens",
		})
	}

	log.Printf("Account created via XRPC: %s (did: %s, domain: %s)", acct.Handle, acct.DID, domainName)

	return c.JSON(http.StatusOK, map[string]any{
		"did":        acct.DID,
		"handle":     acct.Handle,
		"accessJwt":  tokens.AccessJwt,
		"refreshJwt": tokens.RefreshJwt,
	})
}
