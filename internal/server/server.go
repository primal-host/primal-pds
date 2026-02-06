// Package server provides the HTTP server for primal-pds, built on
// Echo v4. It hosts both the standard AT Protocol XRPC endpoints and
// the custom management API (host.primal.pds.*).
package server

import (
	"context"
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/primal-host/primal-pds/internal/config"
	"github.com/primal-host/primal-pds/internal/domain"
)

// Server wraps the Echo instance and application dependencies.
type Server struct {
	echo    *echo.Echo
	cfg     *config.Config
	domains *domain.Store
}

// New creates a configured Echo server with all routes registered.
func New(cfg *config.Config, domains *domain.Store) *Server {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true // We log the listen address ourselves.

	e.Use(middleware.Recover())
	e.Use(middleware.Logger())

	s := &Server{
		echo:    e,
		cfg:     cfg,
		domains: domains,
	}

	s.registerRoutes()
	return s
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
