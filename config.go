package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// AppConfig holds the application configuration
type AppConfig struct {
	DSN        string
	ServerMode string
	Name       string
	Port       int
}

// ParseConfig parses configuration from command line flags and environment variables
// Priority: CLI flags > Environment variables > .env file
func ParseConfig() (*AppConfig, error) {
	// Define command line flags
	dsn := flag.String("dsn", "", "Database connection DSN (e.g., mysql://user:pass@host:3306/db)")
	mode := flag.String("mode", "", "Server mode: stdio, sse, http (default: stdio)")
	name := flag.String("name", "", "Instance name for logging (optional)")
	port := flag.Int("port", 8088, "Port for SSE/HTTP server (default: 8088)")

	flag.Parse()

	config := &AppConfig{}

	// DSN priority: --dsn flag > DATABASE_URL env
	if *dsn != "" {
		config.DSN = *dsn
	} else if envDSN := os.Getenv("DATABASE_URL"); envDSN != "" {
		config.DSN = envDSN
	}

	// Server mode priority: --mode flag > SERVER_MODE env
	if *mode != "" {
		config.ServerMode = *mode
	} else if envMode := os.Getenv("SERVER_MODE"); envMode != "" {
		config.ServerMode = envMode
	}

	// Instance name priority: --name flag > DB_NAME env
	if *name != "" {
		config.Name = *name
	} else if envName := os.Getenv("DB_NAME"); envName != "" {
		config.Name = envName
	}

	// Port priority: --port flag > SERVER_PORT env
	if *port != 8088 {
		config.Port = *port
	} else if envPort := os.Getenv("SERVER_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			config.Port = p
		} else {
			config.Port = 8088 // default
		}
	} else {
		config.Port = 8088
	}

	// Default server mode
	if config.ServerMode == "" {
		config.ServerMode = "stdio"
	}

	// Validate DSN
	if config.DSN == "" {
		return nil, fmt.Errorf("DSN is required. Use --dsn flag or set DATABASE_URL environment variable.\n" +
			"Supported formats:\n" +
			"  mysql://user:pass@host:3306/dbname\n" +
			"  postgres://user:pass@host:5432/dbname\n" +
			"  sqlite:///path/to/db.sqlite\n" +
			"  sqlserver://user:pass@host:1433/dbname")
	}

	// Validate server mode
	validModes := map[string]bool{"stdio": true, "sse": true, "http": true}
	if !validModes[config.ServerMode] {
		return nil, fmt.Errorf("invalid server mode: %s (use stdio, sse, or http)", config.ServerMode)
	}

	return config, nil
}

// MaskDSN masks sensitive information in DSN for logging
func MaskDSN(dsn string) string {
	if dsn == "" {
		return ""
	}

	// Try to mask password in URL format
	for _, prefix := range []string{"mysql://", "postgres://", "postgresql://", "sqlserver://"} {
		if strings.HasPrefix(dsn, prefix) {
			parts := strings.SplitN(dsn[len(prefix):], "@", 2)
			if len(parts) == 2 {
				creds := parts[0]
				if idx := strings.Index(creds, ":"); idx != -1 {
					creds = creds[:idx] + ":***"
				}
				return prefix + creds + "@" + parts[1]
			}
		}
	}

	// For SQLite, just return as-is
	return dsn
}
