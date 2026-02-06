// primal-pds is a multi-tenant AT Protocol Personal Data Server.
//
// It reads configuration from db.json in the working directory, connects
// to PostgreSQL, bootstraps the schema, generates Traefik routing config
// for active domains, and starts an HTTP server with both standard AT
// Protocol endpoints and a management API.
//
// Usage:
//
//	./primal-pds              # reads ./db.json, starts server
//	docker compose up -d      # runs via Docker with mounted config
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/primal-host/primal-pds/internal/config"
	"github.com/primal-host/primal-pds/internal/database"
	"github.com/primal-host/primal-pds/internal/domain"
	"github.com/primal-host/primal-pds/internal/server"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("primal-pds starting...")

	// Load configuration.
	cfg, err := config.Load("db.json")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	log.Printf("Config loaded (listen=%s db=%s/%s)", cfg.ListenAddr, cfg.DBConn, cfg.DBName)

	// Root context cancelled on SIGINT or SIGTERM.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received %v, shutting down...", sig)
		cancel()
	}()

	// Connect to PostgreSQL and bootstrap schema.
	db, err := database.Open(ctx, cfg.ConnString())
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()
	log.Println("Database connected, schema bootstrapped")

	// Initialize the domain store and write Traefik config to match
	// the current database state.
	domains := domain.NewStore(db)
	if err := domains.WriteTraefikConfig(ctx, cfg.TraefikConfigDir); err != nil {
		log.Printf("Warning: initial Traefik config write failed: %v", err)
	} else {
		log.Printf("Traefik config written to %s", cfg.TraefikConfigDir)
	}

	// Start the HTTP server (blocks until context is cancelled).
	srv := server.New(cfg, domains)
	if err := srv.Start(ctx); err != nil {
		log.Fatalf("Server error: %v", err)
	}

	log.Println("primal-pds stopped")
}
