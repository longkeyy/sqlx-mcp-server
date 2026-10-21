package config

import (
	"fmt"
	"net/url"
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

	envDatabaseURL          = "DB_URL"
	envDatabaseName         = "DB_NAME"
	envDatabaseMemo         = "DB_MEMO"
	envDefaultDatabase      = "DEFAULT_DB"
	envListen               = "LISTEN"
	envAuthToken            = "AUTH_TOKEN"
	envDatabaseMaxOpenConns = "DB_MAX_OPEN_CONNS"
	envDatabaseMaxIdleConns = "DB_MAX_IDLE_CONNS"
)

var databaseURLEnvPattern = regexp.MustCompile(`^DB_(.+)_URL$`)

// AppConfig holds the application configuration resolved from the command
// line and environment.
type AppConfig struct {
	DSN       string
	Name      string
	Listen    string // HTTP bind address; always empty in stdio mode
	AuthToken string
	// DataDir is the cleaned --data-dir/DATA_DIR value. Parsing never touches
	// the filesystem; call PrepareDataDir before using it.
	DataDir string
	// Mode is ModeStdio or ModeServe.
	Mode string
	// LegacyHTTP marks the deprecated no-subcommand HTTP startup.
	LegacyHTTP bool
	// ExplicitStdio is true when the stdio subcommand was given.
	ExplicitStdio bool
	// Notices are operator-facing startup messages (e.g. deprecations).
	Notices         []string
	Databases       []DatabaseConfig
	DefaultDatabase string
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
	return ParseArgs(os.Args[1:], EnvMapFromOS())
}

// EnvMapFromOS returns the process environment as a map.
func EnvMapFromOS() map[string]string {
	return envMapFromOS()
}

func parseDatabaseConfigs(defaultDSN, defaultName, defaultMemo string, env map[string]string, allowEmpty bool) ([]DatabaseConfig, string, error) {
	databasesByID := map[string]DatabaseConfig{}
	// sourceByID names where each database came from so configuration errors
	// can point at the offending variables without echoing DSN credentials.
	sourceByID := map[string]string{}
	var ids []string
	globalPool, err := parsePoolConfig(env, "")
	if err != nil {
		return nil, "", err
	}

	if defaultDSN != "" {
		name := defaultName
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
			Memo:         defaultMemo,
			DSN:          defaultDSN,
			MaxOpenConns: pool.maxOpen,
			MaxIdleConns: pool.maxIdle,
		}
		sourceByID[defaultDatabaseID] = "--dsn/" + envDatabaseURL
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
			return nil, "", fmt.Errorf("%s is reserved; use DB_URL for the default database", key)
		}
		dsn := strings.TrimSpace(env[key])
		if dsn == "" {
			continue
		}
		name := strings.TrimSpace(env[fmt.Sprintf("DB_%s_NAME", rawID)])
		if name == "" {
			name = "sql-mcp-" + id
		}
		memo := strings.TrimSpace(env[fmt.Sprintf("DB_%s_MEMO", rawID)])
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
		if _, exists := databasesByID[id]; exists {
			return nil, "", fmt.Errorf("database id %q is configured more than once (%s and %s)", id, sourceByID[id], key)
		}
		ids = append(ids, id)
		sourceByID[id] = key
		databasesByID[id] = DatabaseConfig{
			ID:           id,
			Name:         name,
			Memo:         memo,
			DSN:          dsn,
			MaxOpenConns: pool.maxOpen,
			MaxIdleConns: pool.maxIdle,
		}
	}

	sort.Strings(ids)
	if len(ids) == 0 {
		if allowEmpty {
			if rawDefault := strings.TrimSpace(env[envDefaultDatabase]); rawDefault != "" {
				return nil, "", fmt.Errorf("DEFAULT_DB %q does not match any configured database", rawDefault)
			}
			return nil, "", nil
		}
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

	defaultID := ""
	if rawDefault := strings.TrimSpace(env[envDefaultDatabase]); rawDefault != "" {
		normalized, err := normalizeDatabaseID(rawDefault)
		if err != nil {
			return nil, "", fmt.Errorf("invalid DEFAULT_DB: %w", err)
		}
		if _, exists := databasesByID[normalized]; !exists {
			return nil, "", fmt.Errorf("DEFAULT_DB %q does not match any configured database", rawDefault)
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
		openKey = fmt.Sprintf("DB_%s_MAX_OPEN_CONNS", rawID)
		idleKey = fmt.Sprintf("DB_%s_MAX_IDLE_CONNS", rawID)
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

// MaskDSN hides credentials in a DSN so it can be logged or reported.
//
// The userinfo password of a URL-style DSN and password-like query parameters
// are replaced by "***". The last "@" ends the userinfo so passwords that
// contain "@" are never partially revealed; when in doubt the function masks
// more rather than less. DSNs without credentials (e.g. sqlite) are unchanged.
func MaskDSN(dsn string) string {
	if dsn == "" {
		return ""
	}

	masked := dsn
	if scheme, rest, ok := strings.Cut(dsn, "://"); ok {
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			user, _, hasPassword := strings.Cut(rest[:at], ":")
			if hasPassword {
				masked = scheme + "://" + user + ":***@" + rest[at+1:]
			}
		}
	}
	return secretQueryParamPattern.ReplaceAllString(masked, "${1}***")
}

// RedactDSN removes a DSN and the credentials it contains from text, such as
// a driver or URL-parser error, before the text is logged or returned to a
// client. Raw, quoted (%q) and percent-decoded forms are all replaced.
func RedactDSN(text, dsn string) string {
	if text == "" || dsn == "" {
		return text
	}
	text = replaceAllForms(text, dsn, MaskDSN(dsn))
	for _, secret := range dsnSecrets(dsn) {
		text = replaceAllForms(text, secret, "***")
	}
	return text
}

func replaceAllForms(text, old, replacement string) string {
	text = strings.ReplaceAll(text, old, replacement)
	quoted := strconv.Quote(old)
	if inner := quoted[1 : len(quoted)-1]; inner != old {
		text = strings.ReplaceAll(text, inner, replacement)
	}
	return text
}

// dsnSecrets returns the credential values embedded in a DSN: the userinfo
// password and password-like query parameters, as written and decoded.
func dsnSecrets(dsn string) []string {
	var secrets []string
	add := func(value string) {
		if value == "" {
			return
		}
		secrets = append(secrets, value)
		if decoded, err := url.QueryUnescape(value); err == nil && decoded != value && decoded != "" {
			secrets = append(secrets, decoded)
		}
	}
	if _, rest, ok := strings.Cut(dsn, "://"); ok {
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			if _, password, ok := strings.Cut(rest[:at], ":"); ok {
				add(password)
			}
		}
	}
	for _, match := range secretQueryParamPattern.FindAllStringSubmatch(dsn, -1) {
		add(match[2])
	}
	return secrets
}

// secretQueryParamPattern matches credential-bearing DSN parameters such as
// sqlserver's password=... or clickhouse's password=...; group 1 is the key
// and separator, group 2 the value.
var secretQueryParamPattern = regexp.MustCompile(`(?i)([?&;](?:password|passwd|pwd|secret|token|access_token|api_key)=)([^&;#]*)`)
