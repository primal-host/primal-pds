// Package config handles loading and validating the application
// configuration from a db.json file.
//
// The configuration file is expected to be a JSON object with database
// connection details, HTTP listen address, Traefik integration settings,
// and an admin key for the management API.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
)

// Config holds all application configuration loaded from db.json.
// The file is read once at startup; changes require a restart.
type Config struct {
	// DBConn is the PostgreSQL host:port (e.g., "infra-postgres:5432").
	DBConn string `json:"dbConn"`

	// DBName is the PostgreSQL database name.
	DBName string `json:"dbName"`

	// DBUser is the PostgreSQL username.
	DBUser string `json:"dbUser"`

	// DBPass is the PostgreSQL password.
	DBPass string `json:"dbPass"`

	// ListenAddr is the HTTP listen address (default ":3000").
	ListenAddr string `json:"listenAddr"`

	// TraefikConfigDir is the directory where Traefik dynamic config YAML
	// files are written. Traefik's file provider should watch this directory
	// so route changes take effect automatically.
	TraefikConfigDir string `json:"traefikConfigDir"`

	// AdminKey is a shared secret for authenticating management API calls.
	// Clients send it as "Authorization: Bearer <adminKey>".
	AdminKey string `json:"adminKey"`

	// PLCEndpoint is the PLC directory URL (e.g., "https://plc.directory").
	// When set, new accounts get proper did:plc DIDs derived from their
	// signing key. When empty, random DIDs are generated (local-only).
	PLCEndpoint string `json:"plcEndpoint,omitempty"`
}

// Load reads and parses configuration from the given file path.
// It returns an error if the file cannot be read, parsed, or is missing
// required fields.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":3000"
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// validate checks that all required fields are present.
func (c *Config) validate() error {
	switch {
	case c.DBConn == "":
		return fmt.Errorf("config: dbConn is required")
	case c.DBName == "":
		return fmt.Errorf("config: dbName is required")
	case c.DBUser == "":
		return fmt.Errorf("config: dbUser is required")
	case c.DBPass == "":
		return fmt.Errorf("config: dbPass is required")
	case c.TraefikConfigDir == "":
		return fmt.Errorf("config: traefikConfigDir is required")
	case c.AdminKey == "":
		return fmt.Errorf("config: adminKey is required")
	}
	return nil
}

// ConnString builds a PostgreSQL connection URI from the config fields.
// The password is URL-encoded to handle special characters safely.
func (c *Config) ConnString() string {
	return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable",
		url.QueryEscape(c.DBUser),
		url.QueryEscape(c.DBPass),
		c.DBConn,
		url.QueryEscape(c.DBName),
	)
}

// ConnBase returns a connection string template without a database name.
// Used by PoolManager to construct per-tenant connection strings.
func (c *Config) ConnBase() string {
	return fmt.Sprintf("postgres://%s:%s@%s",
		url.QueryEscape(c.DBUser),
		url.QueryEscape(c.DBPass),
		c.DBConn,
	)
}
