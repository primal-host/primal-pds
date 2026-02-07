package server

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/primal-host/primal-pds/internal/account"
)

// wsUpgrader allows any origin — the firehose is a public endpoint.
var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

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

// handleSubscribeRepos is the AT Protocol firehose WebSocket endpoint.
// It upgrades to WebSocket, subscribes to the EventManager, and streams
// pre-serialized CBOR frames. An optional cursor query parameter enables
// replay of historical events.
// GET /xrpc/com.atproto.sync.subscribeRepos?cursor=...
func (s *Server) handleSubscribeRepos(c echo.Context) error {
	if s.events == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"error":   "ServiceUnavailable",
			"message": "Firehose not available",
		})
	}

	// Parse optional cursor.
	var since *int64
	if cursorStr := c.QueryParam("cursor"); cursorStr != "" {
		n, err := strconv.ParseInt(cursorStr, 10, 64)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error":   "InvalidRequest",
				"message": "cursor must be an integer",
			})
		}
		since = &n
	}

	// Upgrade to WebSocket.
	ws, err := wsUpgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return nil
	}
	defer ws.Close()

	ctx := c.Request().Context()

	// Subscribe to event stream.
	ch, cancel, err := s.events.Subscribe(ctx, since)
	if err != nil {
		log.Printf("Subscribe error: %v", err)
		return nil
	}
	defer cancel()

	// Read goroutine: detects client disconnect.
	disconnected := make(chan struct{})
	go func() {
		defer close(disconnected)
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Write loop: send frames to client.
	for {
		select {
		case frame, ok := <-ch:
			if !ok {
				// Channel closed — slow consumer or shutdown.
				return nil
			}
			if err := ws.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				return nil
			}
		case <-disconnected:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}
