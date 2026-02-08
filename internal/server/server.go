// Package server provides the HTTP server for primal-pds, built on
// Echo v4. It hosts both the standard AT Protocol XRPC endpoints and
// the custom management API (host.primal.pds.*).
package server

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/primal-host/primal-pds/internal/auth"
	"github.com/primal-host/primal-pds/internal/blob"
	"github.com/primal-host/primal-pds/internal/config"
	"github.com/primal-host/primal-pds/internal/database"
	"github.com/primal-host/primal-pds/internal/domain"
	"github.com/primal-host/primal-pds/internal/events"
	"github.com/primal-host/primal-pds/internal/repo"
)

// Server wraps the Echo instance and application dependencies.
type Server struct {
	echo    *echo.Echo
	cfg     *config.Config
	mgmtDB  *database.ManagementDB
	pools   *database.PoolManager
	domains *domain.Store
	repos   *repo.Manager
	events  *events.Manager
	jwt     *auth.JWTManager
	blobs   *blob.Store
}

// New creates a configured Echo server with all routes registered.
func New(cfg *config.Config, mgmtDB *database.ManagementDB, pools *database.PoolManager, domains *domain.Store, repos *repo.Manager, evts *events.Manager, jwtMgr *auth.JWTManager) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true // We log the listen address ourselves.

	e.Use(middleware.Recover())
	e.Use(middleware.Logger())

	s := &Server{
		echo:    e,
		cfg:     cfg,
		mgmtDB:  mgmtDB,
		pools:   pools,
		domains: domains,
		repos:   repos,
		events:  evts,
		jwt:     jwtMgr,
		blobs:   blob.NewStore(),
	}

	s.registerRoutes()
	return s
}

// authContext holds the authenticated caller's identity.
type authContext struct {
	DID     string
	IsAdmin bool
}

const authContextKey = "auth"

// getAuth retrieves the auth context set by middleware.
func getAuth(c echo.Context) *authContext {
	if ac, ok := c.Get(authContextKey).(*authContext); ok {
		return ac
	}
	return nil
}

// requireAuth is middleware that validates a Bearer token as either an
// admin key or a JWT access token. Sets authContext on the request.
func (s *Server) requireAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		token := extractBearer(c)
		if token == "" {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error":   "AuthRequired",
				"message": "Authorization header with Bearer token is required",
			})
		}

		// Try admin key first.
		if token == s.cfg.AdminKey {
			c.Set(authContextKey, &authContext{IsAdmin: true})
			return next(c)
		}

		// Try JWT access token.
		did, err := s.jwt.ValidateAccessToken(token)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error":   "InvalidToken",
				"message": "Invalid or expired access token",
			})
		}

		c.Set(authContextKey, &authContext{DID: did})
		return next(c)
	}
}

// requireRefresh is middleware that validates a Bearer token as a JWT
// refresh token. Sets authContext on the request.
func (s *Server) requireRefresh(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		token := extractBearer(c)
		if token == "" {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error":   "AuthRequired",
				"message": "Authorization header with Bearer token is required",
			})
		}

		did, err := s.jwt.ValidateRefreshToken(token)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error":   "InvalidToken",
				"message": "Invalid or expired refresh token",
			})
		}

		c.Set(authContextKey, &authContext{DID: did})
		return next(c)
	}
}

// extractBearer extracts the Bearer token from the Authorization header.
func extractBearer(c echo.Context) string {
	h := c.Request().Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return h[len(prefix):]
	}
	return ""
}

// Start begins listening for HTTP requests. It blocks until the context
// is cancelled, then performs a graceful shutdown allowing in-flight
// requests to complete.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		log.Printf("Listening on %s", s.cfg.ListenAddr)
		if err := s.echo.Start(s.cfg.ListenAddr); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Println("Shutting down HTTP server...")
		return s.echo.Shutdown(context.Background())
	}
}

// adminAuth is middleware that validates the Authorization header against
// the configured admin key. Management API endpoints are protected by
// this middleware.
func (s *Server) adminAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := c.Request().Header.Get("Authorization")
		if auth == "" {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error":   "AuthRequired",
				"message": "Authorization header is required",
			})
		}

		const prefix = "Bearer "
		if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix {
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error":   "InvalidAuth",
				"message": "Authorization header must use Bearer scheme",
			})
		}

		if auth[len(prefix):] != s.cfg.AdminKey {
			return c.JSON(http.StatusForbidden, map[string]string{
				"error":   "Forbidden",
				"message": "Invalid admin key",
			})
		}

		return next(c)
	}
}
