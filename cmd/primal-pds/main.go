// primal-pds is a multi-tenant AT Protocol Personal Data Server.
//
// It reads configuration from db.json in the working directory, connects
// to PostgreSQL, bootstraps the management schema, opens per-domain
// tenant databases, generates Traefik routing config for active domains,
// and starts an HTTP server with both standard AT Protocol endpoints and
// a management API.
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

	"github.com/primal-host/primal-pds/internal/account"
	"github.com/primal-host/primal-pds/internal/config"
	"github.com/primal-host/primal-pds/internal/database"
	"github.com/primal-host/primal-pds/internal/domain"
	"github.com/primal-host/primal-pds/internal/repo"
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

	// Open management database and bootstrap management schema.
	mgmtDB, err := database.OpenManagement(ctx, cfg.ConnString(), cfg.ConnBase())
	if err != nil {
		log.Fatalf("Failed to connect to management database: %v", err)
	}
	defer mgmtDB.Close()
	log.Println("Management database connected, schema bootstrapped")

	// Initialize pool manager for tenant databases.
	pools := database.NewPoolManager(cfg.ConnBase())
	defer pools.Close()

	// Initialize domain store.
	domains := domain.NewStore(mgmtDB)

	// Load existing domains and open tenant pools.
	allDomains, err := domains.List(ctx)
	if err != nil {
		log.Fatalf("Failed to list domains: %v", err)
	}

	for _, d := range allDomains {
		if err := pools.Add(ctx, d.Domain, d.DBName); err != nil {
			log.Printf("Warning: failed to open tenant pool for %s: %v", d.Domain, err)
			continue
		}
		log.Printf("Tenant pool opened: %s -> %s", d.Domain, d.DBName)
	}

	// Initialize repos for existing accounts in each tenant DB.
	repos := repo.NewManager()
	for _, d := range allDomains {
		pool := pools.Get(d.Domain)
		if pool == nil {
			continue
		}

		tenantAccounts := account.NewStore(&database.DB{Pool: pool})
		accts, err := tenantAccounts.List(ctx)
		if err != nil {
			log.Printf("Warning: failed to list accounts for %s: %v", d.Domain, err)
			continue
		}

		for _, acct := range accts {
			if acct.SigningKey == "" {
				continue
			}
			if err := repos.InitRepo(ctx, pool, acct.DID, acct.SigningKey); err != nil {
				log.Printf("Warning: failed to init repo for %s: %v", acct.DID, err)
			}
		}
		log.Printf("Repos initialized for %d accounts in %s", len(accts), d.Domain)
	}

	// Write Traefik config to match current database state.
	if err := domains.WriteTraefikConfig(ctx, cfg.TraefikConfigDir); err != nil {
		log.Printf("Warning: initial Traefik config write failed: %v", err)
	} else {
		log.Printf("Traefik config written to %s", cfg.TraefikConfigDir)
	}

	// Start the HTTP server (blocks until context is cancelled).
	srv := server.New(cfg, mgmtDB, pools, domains, repos)
	if err := srv.Start(ctx); err != nil {
		log.Fatalf("Server error: %v", err)
	}

	log.Println("primal-pds stopped")
}
