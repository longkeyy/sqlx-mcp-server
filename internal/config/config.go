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

	"github.com/longkeyy/sqlx-mcp-server/internal/dsn"
)

const (
	defaultDatabaseID = "default"

	defaultMaxOpenConns = 25
	defaultMaxIdleConns = 1

	envDatabaseURL          = "DB_URL"
	envDatabaseName         = "DB_NAME"
	envDatabaseMemo         = "DB_MEMO"
	envDefaultDatabase      = "DEFAULT_DB"
	envListen               = "LISTEN"
	envAuthToken            = "AUTH_TOKEN"
	envDatabaseMaxOpenConns = "DB_MAX_OPEN_CONNS"
	envDatabaseMaxIdleConns = "DB_MAX_IDLE_CONNS"
)

// AppConfig holds the application configuration
type AppConfig struct {
	DSN             string
	Name            string
	Listen          string
	AuthToken       string
	Databases       []DatabaseConfig
	DefaultDatabase string
	// Warnings holds non-fatal configuration problems, such as a deprecated
	// environment variable still being used. The caller should log them.
	Warnings []string
}

type DatabaseConfig struct {
	ID           string
	Name         string
	Memo         string
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
	name := flags.String("name", "", "Instance name for MCP server display/logging (optional)")
	listen := flags.String("listen", "", "Network listen address for HTTP/SSE server (e.g., :8088 or 127.0.0.1:8088). Empty uses stdio.")
	authToken := flags.String("auth-token", "", "HTTP/SSE bearer token. Empty disables built-in authentication.")

	if err := flags.Parse(args); err != nil {
		return nil, err
	}

	config := &AppConfig{}
	lookupEnv := func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
	// resolver reads settings that also have a pre-rename alias.
	resolver := newConfigResolver(env)

	// DSN priority: --dsn flag > DB_URL env > DATABASE_URL env (deprecated)
	if *dsn != "" {
		config.DSN = *dsn
	} else {
		envDSN, err := resolver.resolve(envDatabaseURL, legacyEnvDatabaseURL, trimSpace)
		if err != nil {
			return nil, err
		}
		config.DSN = envDSN
	}

	// Instance name priority: --name flag > INSTANCE_NAME env (deprecated) > DB_NAME env
	nameKey := ""
	if *name != "" {
		config.Name = *name
		nameKey = "--name"
	} else {
		config.Name = resolver.preferLegacy(envDatabaseName, legacyEnvInstanceName)
		switch {
		case resolver.trim(legacyEnvInstanceName) != "":
			nameKey = legacyEnvInstanceName
		case resolver.trim(envDatabaseName) != "":
			nameKey = envDatabaseName
		}
	}

	if *listen != "" {
		config.Listen = strings.TrimSpace(*listen)
	} else if envListen, ok := lookupEnv(envListen); ok {
		config.Listen = strings.TrimSpace(envListen)
	}
	if *authToken != "" {
		config.AuthToken = strings.TrimSpace(*authToken)
	} else if envAuthToken, ok := lookupEnv(envAuthToken); ok {
		config.AuthToken = strings.TrimSpace(envAuthToken)
	}

	// DEFAULT_DB priority: DEFAULT_DB env > DEFAULT_DATABASE env (deprecated)
	defaultDatabase, err := resolver.resolve(envDefaultDatabase, legacyEnvDefaultDatabase, normalizeDefaultDatabase)
	if err != nil {
		return nil, err
	}

	databases, defaultID, err := parseDatabaseConfigs(resolver, globalDatabaseSettings{
		dsn:        config.DSN,
		name:       config.Name,
		nameKey:    nameKey,
		memo:       resolver.trim(envDatabaseMemo),
		rawDefault: defaultDatabase,
	})
	if err != nil {
		return nil, err
	}
	config.Databases = databases
	config.DefaultDatabase = defaultID
	config.Warnings = resolver.warningsText()
	if config.DSN == "" && defaultID != "" {
		config.DSN = databaseByID(databases, defaultID).DSN
	}
	if config.Name == "" && defaultID != "" {
		config.Name = databaseByID(databases, defaultID).Name
	}

	return config, nil
}

// globalDatabaseSettings hold the settings that describe the default database
// when DB_URL is not set. nameKey records which flag or variable supplied the
// name so a warning can point at the key the operator actually set.
type globalDatabaseSettings struct {
	dsn        string
	name       string
	nameKey    string
	memo       string
	rawDefault string // already-resolved DEFAULT_DB / DEFAULT_DATABASE value
}

// parseDatabaseConfigs builds the database list from the default DSN plus every
// DB_{ID}_URL / DATABASE_{ID}_URL key. resolver collects any warnings.
func parseDatabaseConfigs(resolver *configResolver, global globalDatabaseSettings) ([]DatabaseConfig, string, error) {
	databasesByID := map[string]DatabaseConfig{}
	// Which named databases set a display setting of their own; the global
	// name and memo only fill the gap.
	perIDName := map[string]bool{}
	perIDMemo := map[string]bool{}
	var ids []string
	globalPool, err := resolver.parsePoolConfig("", "")
	if err != nil {
		return nil, "", err
	}

	if global.dsn != "" {
		name := global.name
		if name == "" {
			name = "sql-mcp-default"
		}
		pool, err := resolver.parsePoolConfig(defaultDatabaseID, "DEFAULT")
		if err != nil {
			return nil, "", err
		}
		pool = pool.inheriting(globalPool)
		if err := validatePool(defaultDatabaseID, pool); err != nil {
			return nil, "", err
		}
		databasesByID[defaultDatabaseID] = DatabaseConfig{
			ID:           defaultDatabaseID,
			Name:         name,
			Memo:         global.memo,
			DSN:          global.dsn,
			MaxOpenConns: pool.maxOpen,
			MaxIdleConns: pool.maxIdle,
		}
		ids = append(ids, defaultDatabaseID)
	}

	namedURLs, err := discoverDatabaseURLs(resolver)
	if err != nil {
		return nil, "", err
	}
	resolver.warnUnusedDatabaseSettings(namedURLs, global.dsn != "")
	namedIDs := make([]string, 0, len(namedURLs))
	for id := range namedURLs {
		namedIDs = append(namedIDs, id)
	}
	sort.Strings(namedIDs)

	for _, id := range namedIDs {
		source := namedURLs[id]
		name, err := resolver.resolveDisplaySetting(source.id, source.rawID, settingName)
		if err != nil {
			return nil, "", err
		}
		perIDName[id] = name != ""
		if name == "" {
			name = "sql-mcp-" + id
		}
		memo, err := resolver.resolveDisplaySetting(source.id, source.rawID, settingMemo)
		if err != nil {
			return nil, "", err
		}
		perIDMemo[id] = memo != ""
		pool, err := resolver.parsePoolConfig(source.id, source.rawID)
		if err != nil {
			return nil, "", err
		}
		pool = pool.inheriting(globalPool)
		if err := validatePool(id, pool); err != nil {
			return nil, "", err
		}
		ids = append(ids, id)
		databasesByID[id] = DatabaseConfig{
			ID:           id,
			Name:         name,
			Memo:         memo,
			DSN:          source.dsn,
			MaxOpenConns: pool.maxOpen,
			MaxIdleConns: pool.maxIdle,
		}
	}

	sort.Strings(ids)
	if len(ids) == 0 {
		return nil, "", fmt.Errorf("DSN is required. Use --dsn, DB_URL, or DB_{ID}_URL environment variables.\n" +
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

	explicitDefault := global.rawDefault != ""
	defaultID := ""
	if explicitDefault {
		defaultKey := resolver.activeKey(envDefaultDatabase, legacyEnvDefaultDatabase)
		normalized, err := normalizeDatabaseID(global.rawDefault)
		if err != nil {
			return nil, "", fmt.Errorf("invalid %s: %w", defaultKey, err)
		}
		if _, exists := databasesByID[normalized]; !exists {
			return nil, "", fmt.Errorf("%s %q does not match any configured database", defaultKey, global.rawDefault)
		}
		defaultID = normalized
	} else if _, exists := databasesByID[defaultDatabaseID]; exists {
		defaultID = defaultDatabaseID
	} else {
		if len(ids) > 1 {
			resolver.warn("%s is not set; using %q as the default database", envDefaultDatabase, ids[0])
		}
		defaultID = ids[0]
	}

	if global.dsn == "" {
		applyGlobalDisplay(resolver, databasesByID, perIDName, perIDMemo, explicitDefault, len(ids), defaultID, namedURLs[defaultID].rawID, global)
	}

	databases := make([]DatabaseConfig, 0, len(ids))
	for _, id := range ids {
		databases = append(databases, databasesByID[id])
	}
	return databases, defaultID, nil
}

// applyGlobalDisplay fills the display name and memo of a named default
// database from the global settings, which describe the default database
// whenever DB_URL is not set. A per-ID {ID}_NAME or {ID}_MEMO always wins.
// When the default was picked implicitly among several databases, nothing
// suggests the global values describe it, so they warn instead of applying.
func applyGlobalDisplay(resolver *configResolver, databasesByID map[string]DatabaseConfig, perIDName, perIDMemo map[string]bool, explicitDefault bool, databaseCount int, defaultID, defaultRawID string, global globalDatabaseSettings) {
	applies := explicitDefault || databaseCount == 1
	database := databasesByID[defaultID]

	if global.nameKey != "" && !perIDName[defaultID] {
		if applies {
			database.Name = global.name
		} else {
			resolver.warn("%s is not applied to database %q because %s is not set; set %s or DB_%s_NAME",
				global.nameKey, defaultID, envDefaultDatabase, envDefaultDatabase, defaultRawID)
		}
	}
	if global.memo != "" && !perIDMemo[defaultID] {
		if applies {
			database.Memo = global.memo
		} else {
			resolver.warn("%s is not applied to database %q because %s is not set; set %s or DB_%s_MEMO",
				envDatabaseMemo, defaultID, envDefaultDatabase, envDefaultDatabase, defaultRawID)
		}
	}

	databasesByID[defaultID] = database
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

// MaskDSN masks sensitive information in a DSN for logging.
//
// Deprecated: use dsn.Mask, which also masks credential query parameters.
func MaskDSN(raw string) string {
	return dsn.Mask(raw)
}
