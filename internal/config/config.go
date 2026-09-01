package config

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultDatabaseID = "default"

	defaultMaxOpenConns = 25
	defaultMaxIdleConns = 1

	envDatabaseURL          = "DATABASE_URL"
	envDefaultDatabase      = "DEFAULT_DATABASE"
	envInstanceName         = "INSTANCE_NAME"
	envLegacyDBName         = "DB_NAME"
	envServerMode           = "SERVER_MODE"
	envServerPort           = "SERVER_PORT"
	envDatabaseMaxOpenConns = "DATABASE_MAX_OPEN_CONNS"
	envDatabaseMaxIdleConns = "DATABASE_MAX_IDLE_CONNS"
)

var databaseURLEnvPattern = regexp.MustCompile(`^DATABASE_(.+)_URL$`)

// AppConfig holds the application configuration
type AppConfig struct {
	DSN             string
	ServerMode      string
	Name            string
	Port            int
	Databases       []DatabaseConfig
	DefaultDatabase string
}

type DatabaseConfig struct {
	ID           string
	Name         string
	DSN          string
	MaxOpenConns int
	MaxIdleConns int
}

// Parse reads configuration from command-line flags and environment variables.
// Priority: CLI flags > Environment variables > .env file
func Parse() (*AppConfig, error) {
	return parseConfig(os.Args[1:], envMapFromOS())
}

func parseConfig(args []string, env map[string]string) (*AppConfig, error) {
	flags := flag.NewFlagSet("sql-mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	// Define command line flags
	dsn := flags.String("dsn", "", "Database connection DSN (e.g., mysql://user:pass@host:3306/db)")
	mode := flags.String("mode", "", "Server mode: stdio, sse, http, all (default: stdio)")
	name := flags.String("name", "", "Instance name for MCP server display/logging (optional)")
	port := flags.Int("port", 8088, "Port for SSE/HTTP server (default: 8088)")

	if err := flags.Parse(args); err != nil {
		return nil, err
	}

	config := &AppConfig{}
	lookupEnv := func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}

	// DSN priority: --dsn flag > DATABASE_URL env
	if *dsn != "" {
		config.DSN = *dsn
	} else if envDSN, ok := lookupEnv(envDatabaseURL); ok && envDSN != "" {
		config.DSN = envDSN
	}

	// Server mode priority: --mode flag > SERVER_MODE env
	if *mode != "" {
		config.ServerMode = *mode
	} else if envMode, ok := lookupEnv(envServerMode); ok && envMode != "" {
		config.ServerMode = envMode
	}

	// Instance name priority: --name flag > INSTANCE_NAME env > DB_NAME legacy env
	if *name != "" {
		config.Name = *name
	} else if envName, ok := lookupEnv(envInstanceName); ok && envName != "" {
		config.Name = envName
	} else if envName, ok := lookupEnv(envLegacyDBName); ok && envName != "" {
		config.Name = envName
	}

	// Port priority: --port flag > SERVER_PORT env
	if *port != 8088 {
		config.Port = *port
	} else if envPort, ok := lookupEnv(envServerPort); ok && envPort != "" {
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

	databases, defaultDatabase, err := parseDatabaseConfigs(config.DSN, config.Name, env)
	if err != nil {
		return nil, err
	}
	config.Databases = databases
	config.DefaultDatabase = defaultDatabase
	if config.DSN == "" && defaultDatabase != "" {
		config.DSN = databaseByID(databases, defaultDatabase).DSN
	}
	if config.Name == "" && defaultDatabase != "" {
		config.Name = databaseByID(databases, defaultDatabase).Name
	}

	// Validate server mode
	validModes := map[string]bool{"stdio": true, "sse": true, "http": true, "all": true}
	if !validModes[config.ServerMode] {
		return nil, fmt.Errorf("invalid server mode: %s (use stdio, sse, http, or all)", config.ServerMode)
	}

	return config, nil
}

func parseDatabaseConfigs(legacyDSN, legacyName string, env map[string]string) ([]DatabaseConfig, string, error) {
	databasesByID := map[string]DatabaseConfig{}
	var ids []string
	globalPool, err := parsePoolConfig(env, "")
	if err != nil {
		return nil, "", err
	}

	if legacyDSN != "" {
		name := legacyName
		if name == "" {
			name = "sql-mcp-default"
		}
		pool, err := parsePoolConfig(env, defaultDatabaseID)
		if err != nil {
			return nil, "", err
		}
		if !pool.hasMaxOpen {
			pool.maxOpen = globalPool.maxOpen
		}
		if !pool.hasMaxIdle {
			pool.maxIdle = globalPool.maxIdle
		}
		databasesByID[defaultDatabaseID] = DatabaseConfig{
			ID:           defaultDatabaseID,
			Name:         name,
			DSN:          legacyDSN,
			MaxOpenConns: pool.maxOpen,
			MaxIdleConns: pool.maxIdle,
		}
		ids = append(ids, defaultDatabaseID)
	}

	var envKeys []string
	for key := range env {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)

	for _, key := range envKeys {
		match := databaseURLEnvPattern.FindStringSubmatch(key)
		if match == nil {
			continue
		}
		rawID := match[1]
		id, err := normalizeDatabaseID(rawID)
		if err != nil {
			return nil, "", fmt.Errorf("invalid database env %s: %w", key, err)
		}
		if id == defaultDatabaseID {
			return nil, "", fmt.Errorf("%s is reserved; use DATABASE_URL for the default database", key)
		}
		dsn := strings.TrimSpace(env[key])
		if dsn == "" {
			continue
		}
		name := strings.TrimSpace(env[fmt.Sprintf("DATABASE_%s_NAME", rawID)])
		if name == "" {
			name = "sql-mcp-" + id
		}
		pool, err := parsePoolConfig(env, rawID)
		if err != nil {
			return nil, "", err
		}
		if !pool.hasMaxOpen {
			pool.maxOpen = globalPool.maxOpen
		}
		if !pool.hasMaxIdle {
			pool.maxIdle = globalPool.maxIdle
		}
		if existing, exists := databasesByID[id]; exists {
			return nil, "", fmt.Errorf("database id %q is configured more than once; duplicate DSNs: %s and %s", id, existing.DSN, dsn)
		}
		ids = append(ids, id)
		databasesByID[id] = DatabaseConfig{
			ID:           id,
			Name:         name,
			DSN:          dsn,
			MaxOpenConns: pool.maxOpen,
			MaxIdleConns: pool.maxIdle,
		}
	}

	sort.Strings(ids)
	if len(ids) == 0 {
		return nil, "", fmt.Errorf("DSN is required. Use --dsn, DATABASE_URL, or DATABASE_{ID}_URL environment variables.\n" +
			"Supported formats:\n" +
			"  mysql://user:pass@host:3306/dbname\n" +
			"  mariadb://user:pass@host:3306/dbname\n" +
			"  oceanbase://user:pass@host:3306/dbname\n" +
			"  starrocks://user:pass@host:9030/dbname\n" +
			"  tidb://user:pass@host:4000/dbname\n" +
			"  doris://user:pass@host:9030/dbname\n" +
			"  postgres://user:pass@host:5432/dbname\n" +
			"  cockroach://user:pass@host:26257/dbname\n" +
			"  opengauss://user:pass@host:5432/dbname\n" +
			"  sqlite:///path/to/db.sqlite\n" +
			"  sqlserver://user:pass@host:1433/dbname\n" +
			"  clickhouse://user:pass@host:9000/dbname")
	}

	defaultID := ""
	if rawDefault := strings.TrimSpace(env[envDefaultDatabase]); rawDefault != "" {
		normalized, err := normalizeDatabaseID(rawDefault)
		if err != nil {
			return nil, "", fmt.Errorf("invalid DEFAULT_DATABASE: %w", err)
		}
		if _, exists := databasesByID[normalized]; !exists {
			return nil, "", fmt.Errorf("DEFAULT_DATABASE %q does not match any configured database", rawDefault)
		}
		defaultID = normalized
	} else if _, exists := databasesByID[defaultDatabaseID]; exists {
		defaultID = defaultDatabaseID
	} else {
		defaultID = ids[0]
	}

	databases := make([]DatabaseConfig, 0, len(ids))
	for _, id := range ids {
		databases = append(databases, databasesByID[id])
	}
	return databases, defaultID, nil
}

type poolConfig struct {
	maxOpen    int
	maxIdle    int
	hasMaxOpen bool
	hasMaxIdle bool
}

func parsePoolConfig(env map[string]string, rawID string) (poolConfig, error) {
	pool := poolConfig{
		maxOpen: defaultMaxOpenConns,
		maxIdle: defaultMaxIdleConns,
	}

	openKey := envDatabaseMaxOpenConns
	idleKey := envDatabaseMaxIdleConns
	if rawID != "" {
		openKey = fmt.Sprintf("DATABASE_%s_MAX_OPEN_CONNS", rawID)
		idleKey = fmt.Sprintf("DATABASE_%s_MAX_IDLE_CONNS", rawID)
	}

	if value, ok := env[openKey]; ok && strings.TrimSpace(value) != "" {
		parsed, err := parsePositiveIntEnv(openKey, value)
		if err != nil {
			return pool, err
		}
		pool.maxOpen = parsed
		pool.hasMaxOpen = true
	}
	if value, ok := env[idleKey]; ok && strings.TrimSpace(value) != "" {
		parsed, err := parsePositiveIntEnv(idleKey, value)
		if err != nil {
			return pool, err
		}
		pool.maxIdle = parsed
		pool.hasMaxIdle = true
	}
	if pool.maxIdle > pool.maxOpen {
		return pool, fmt.Errorf("%s must be less than or equal to %s", idleKey, openKey)
	}
	return pool, nil
}

func parsePositiveIntEnv(key, value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func normalizeDatabaseID(input string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(input))
	if id == "" {
		return "", fmt.Errorf("database id must not be empty")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`).MatchString(id) {
		return "", fmt.Errorf("database id %q must contain only letters, numbers, underscores, or hyphens, and must start with a letter or number", input)
	}
	return id, nil
}

func databaseByID(databases []DatabaseConfig, id string) DatabaseConfig {
	for _, database := range databases {
		if database.ID == id {
			return database
		}
	}
	return DatabaseConfig{}
}

func envMapFromOS() map[string]string {
	env := make(map[string]string)
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

// MaskDSN masks sensitive information in DSN for logging
func MaskDSN(dsn string) string {
	if dsn == "" {
		return ""
	}

	// Try to mask password in URL format
	for _, prefix := range []string{"mysql://", "mariadb://", "percona://", "tidb://", "doris://", "oceanbase://", "starrocks://", "postgres://", "postgresql://", "cockroach://", "greenplum://", "opengauss://", "mogdb://", "sqlserver://", "clickhouse://"} {
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
