package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Names used before the DB_* rename. They are still accepted so an upgrade
// does not silently drop a configured database, but every use warns.
const (
	legacyEnvDatabaseURL          = "DATABASE_URL"
	legacyEnvInstanceName         = "INSTANCE_NAME"
	legacyEnvDefaultDatabase      = "DEFAULT_DATABASE"
	legacyEnvDatabaseMaxOpenConns = "DATABASE_MAX_OPEN_CONNS"
	legacyEnvDatabaseMaxIdleConns = "DATABASE_MAX_IDLE_CONNS"
)

// databaseSettingEnvPattern indexes both current and legacy per-database keys.
// It does not match the global keys DB_URL, DB_NAME, DB_MEMO, DB_MAX_* because
// those have no id segment; the suffix is matched case-sensitively.
var databaseSettingEnvPattern = regexp.MustCompile(`^(DB|DATABASE)_(.+)_(URL|NAME|MEMO|MAX_OPEN_CONNS|MAX_IDLE_CONNS)$`)

// poolConfig is one database's resolved connection limits. The has* flags
// record whether the value came from a key rather than the built-in default,
// so inheritance and later validation can tell the two apart.
type poolConfig struct {
	maxOpen    int
	maxIdle    int
	hasMaxOpen bool
	hasMaxIdle bool
}

// configResolver reads environment settings, falling back to the pre-rename
// keys and recording a deprecation warning for each alias it uses.
//
// Blank values count as unset. Errors name the conflicting keys but never the
// values, because values can contain credentials. Warnings are ordered by the
// order settings are resolved, which is deterministic.
type configResolver struct {
	env      map[string]string
	warnings []string
}

func newConfigResolver(env map[string]string) *configResolver {
	return &configResolver{env: env}
}

func (r *configResolver) trim(key string) string {
	return strings.TrimSpace(r.env[key])
}

func (r *configResolver) warn(format string, args ...any) {
	r.warnings = append(r.warnings, fmt.Sprintf(format, args...))
}

func (r *configResolver) warningsText() []string {
	if len(r.warnings) == 0 {
		return nil
	}
	return append([]string(nil), r.warnings...)
}

// activeKey names the key holding the effective value of a resolved pair. It is
// used to build error messages that point at the variable the operator set.
func (r *configResolver) activeKey(currentKey, legacyKey string) string {
	if r.trim(currentKey) == "" && r.trim(legacyKey) != "" {
		return legacyKey
	}
	return currentKey
}

// resolve reads currentKey, falling back to legacyKey when currentKey is unset.
//
// If both are set, they must agree after normalize; otherwise this returns an
// error naming both keys. The deprecated legacyKey never wins over an explicit
// currentKey with the same meaning.
func (r *configResolver) resolve(currentKey, legacyKey string, normalize func(string) string) (string, error) {
	current := r.trim(currentKey)
	legacy := r.trim(legacyKey)

	switch {
	case legacy == "":
		return current, nil
	case current == "":
		r.warn("%s is deprecated; use %s instead", legacyKey, currentKey)
		return legacy, nil
	}

	if normalize(current) != normalize(legacy) {
		return "", conflictingAliasError(currentKey, legacyKey)
	}
	r.warn("%s and %s are both set to the same value; %s is deprecated, use %s instead", legacyKey, currentKey, legacyKey, currentKey)
	return current, nil
}

// preferLegacy reads a pair where the deprecated key used to take precedence
// and must keep doing so, as with INSTANCE_NAME over DB_NAME. Both values are
// display-only, so a conflict is a warning rather than an error.
func (r *configResolver) preferLegacy(currentKey, legacyKey string) string {
	current := r.trim(currentKey)
	legacy := r.trim(legacyKey)

	switch {
	case legacy == "":
		return current
	case current == "":
		r.warn("%s is deprecated; use %s instead", legacyKey, currentKey)
	case current == legacy:
		r.warn("%s and %s are both set to the same value; %s is deprecated, use %s instead", legacyKey, currentKey, legacyKey, currentKey)
	default:
		r.warn("%s is deprecated; it overrides %s for compatibility (both are set to different values)", legacyKey, currentKey)
	}
	return legacy
}

// resolveDisplaySetting resolves DB_{ID}_{suffix} against its legacy
// DATABASE_{ID}_{suffix} alias. On a conflict the current key wins with a
// warning: a display name or memo has no security impact, so failing startup
// over it would be disproportionate.
func (r *configResolver) resolveDisplaySetting(rawID, suffix string) string {
	currentKey := fmt.Sprintf("DB_%s_%s", rawID, suffix)
	legacyKey := fmt.Sprintf("DATABASE_%s_%s", rawID, suffix)

	current := r.trim(currentKey)
	legacy := r.trim(legacyKey)

	switch {
	case legacy == "":
		return current
	case current == "":
		r.warn("%s is deprecated; use %s instead", legacyKey, currentKey)
		return legacy
	case current != legacy:
		r.warn("%s and %s are both set to different values; using %s", legacyKey, currentKey, currentKey)
	}
	return current
}

// parsePoolConfig resolves the pool settings for one database. rawID is the id
// as written in the environment so DB_{ID}_* keeps matching exactly as before;
// an empty rawID means the global DB_MAX_* settings. Errors name the keys
// actually in use and never carry values.
func (r *configResolver) parsePoolConfig(rawID string) (poolConfig, error) {
	openKey := envDatabaseMaxOpenConns
	idleKey := envDatabaseMaxIdleConns
	legacyOpenKey := legacyEnvDatabaseMaxOpenConns
	legacyIdleKey := legacyEnvDatabaseMaxIdleConns
	if rawID != "" {
		openKey = fmt.Sprintf("DB_%s_MAX_OPEN_CONNS", rawID)
		idleKey = fmt.Sprintf("DB_%s_MAX_IDLE_CONNS", rawID)
		legacyOpenKey = fmt.Sprintf("DATABASE_%s_MAX_OPEN_CONNS", rawID)
		legacyIdleKey = fmt.Sprintf("DATABASE_%s_MAX_IDLE_CONNS", rawID)
	}

	pool := poolConfig{
		maxOpen: defaultMaxOpenConns,
		maxIdle: defaultMaxIdleConns,
	}

	openValue, err := r.resolve(openKey, legacyOpenKey, trimSpace)
	if err != nil {
		return pool, err
	}
	if openValue != "" {
		parsed, err := parsePositiveIntEnv(r.activeKey(openKey, legacyOpenKey), openValue)
		if err != nil {
			return pool, err
		}
		pool.maxOpen = parsed
		pool.hasMaxOpen = true
	}

	idleValue, err := r.resolve(idleKey, legacyIdleKey, trimSpace)
	if err != nil {
		return pool, err
	}
	if idleValue != "" {
		parsed, err := parsePositiveIntEnv(r.activeKey(idleKey, legacyIdleKey), idleValue)
		if err != nil {
			return pool, err
		}
		pool.maxIdle = parsed
		pool.hasMaxIdle = true
	}

	if pool.maxIdle > pool.maxOpen {
		return pool, fmt.Errorf("%s must be less than or equal to %s", r.activeKey(idleKey, legacyIdleKey), r.activeKey(openKey, legacyOpenKey))
	}
	return pool, nil
}

// databaseURLSource is one resolved DB_{ID}_URL / DATABASE_{ID}_URL setting.
// rawID keeps the spelling used in the environment so the per-id NAME, MEMO and
// pool keys are still found; id is the normalized routing id.
type databaseURLSource struct {
	id    string
	rawID string
	key   string
	dsn   string
}

// discoverDatabaseURLs collects per-database URLs from current and legacy keys.
//
// Grouping happens per origin so two spellings of the same id can be compared
// pairwise, which catches a renamed deployment that still has both. A pair set
// to different DSNs is an error: silently picking one could point the process at
// the wrong database. Equal duplicates use the current key and warn. The id is
// normalized for grouping, so DB_APP_URL and DB_app_URL still collide.
func discoverDatabaseURLs(r *configResolver) (map[string]databaseURLSource, error) {
	current := map[string]databaseURLSource{}
	legacy := map[string]databaseURLSource{}

	for _, key := range sortedEnvKeys(r.env) {
		match := databaseSettingEnvPattern.FindStringSubmatch(key)
		if match == nil || match[3] != "URL" {
			continue
		}
		origin, rawID := match[1], match[2]
		id, err := normalizeDatabaseID(rawID)
		if err != nil {
			return nil, fmt.Errorf("invalid database env %s: %w", key, err)
		}
		if id == defaultDatabaseID {
			return nil, fmt.Errorf("%s is reserved; use %s for the default database", key, envDatabaseURL)
		}
		dsn := strings.TrimSpace(r.env[key])
		if dsn == "" {
			continue
		}

		source := databaseURLSource{id: id, rawID: rawID, key: key, dsn: dsn}
		group := current
		if origin == "DATABASE" {
			group = legacy
		}
		if existing, exists := group[id]; exists {
			return nil, fmt.Errorf("database id %q is configured more than once by %s and %s", id, existing.key, key)
		}
		group[id] = source
	}

	legacyIDs := make([]string, 0, len(legacy))
	for id := range legacy {
		legacyIDs = append(legacyIDs, id)
	}
	sort.Strings(legacyIDs)

	for _, id := range legacyIDs {
		legacySource := legacy[id]
		currentSource, exists := current[id]
		if !exists {
			currentKey := fmt.Sprintf("DB_%s_URL", legacySource.rawID)
			r.warn("%s is deprecated; use %s instead", legacySource.key, currentKey)
			current[id] = legacySource
			continue
		}
		if currentSource.dsn != legacySource.dsn {
			return nil, conflictingAliasError(currentSource.key, legacySource.key)
		}
		r.warn("%s and %s are both set to the same value; %s is deprecated, use %s instead", legacySource.key, currentSource.key, legacySource.key, currentSource.key)
	}

	return current, nil
}

func conflictingAliasError(currentKey, legacyKey string) error {
	return fmt.Errorf("%s and %s are both set to different values; remove the deprecated %s", currentKey, legacyKey, legacyKey)
}

func trimSpace(value string) string {
	return strings.TrimSpace(value)
}

func normalizeDefaultDatabase(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
