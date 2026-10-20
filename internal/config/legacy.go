package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
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

// Suffixes of the per-database DB_{ID}_{SUFFIX} keys.
const (
	settingURL          = "URL"
	settingName         = "NAME"
	settingMemo         = "MEMO"
	settingMaxOpenConns = "MAX_OPEN_CONNS"
	settingMaxIdleConns = "MAX_IDLE_CONNS"
)

// databaseSettingEnvPattern matches both current and legacy per-database keys.
// It does not match the global keys DB_URL, DB_NAME, DB_MEMO, DB_MAX_* because
// those have no id segment. The prefix and suffix are matched case-sensitively;
// the id segment is normalized, so DB_APP_URL and DB_app_NAME belong together.
var databaseSettingEnvPattern = regexp.MustCompile(`^(DB|DATABASE)_(.+)_(URL|NAME|MEMO|MAX_OPEN_CONNS|MAX_IDLE_CONNS)$`)

// databaseSetting is one DB_{ID}_{SUFFIX} or DATABASE_{ID}_{SUFFIX} key.
type databaseSetting struct {
	key    string // as written in the environment
	legacy bool   // DATABASE_ prefix
	rawID  string // id segment as written; spells the keys suggested in messages
	id     string // normalized id; empty when idErr is set
	idErr  error
	suffix string
	value  string // trimmed; blank counts as unset
}

// indexDatabaseSettings classifies every per-database key once, in sorted key
// order. It neither fails nor warns: whether a key is used, ignored with a
// warning or rejected depends on the rest of the configuration, so that is
// left to the callers.
func indexDatabaseSettings(env map[string]string) []databaseSetting {
	var settings []databaseSetting
	for _, key := range sortedEnvKeys(env) {
		match := databaseSettingEnvPattern.FindStringSubmatch(key)
		if match == nil {
			continue
		}
		id, err := normalizeDatabaseID(match[2])
		settings = append(settings, databaseSetting{
			key:    key,
			legacy: match[1] == "DATABASE",
			rawID:  match[2],
			id:     id,
			idErr:  err,
			suffix: match[3],
			value:  strings.TrimSpace(env[key]),
		})
	}
	return settings
}

// poolConfig is one database's resolved connection limits, before or after
// inheritance. The has* flags record whether a value came from a key rather
// than the built-in default, and the key fields name the key that supplied it
// so validation can point at the scope that actually set the limit. An empty
// key means the built-in default applies.
type poolConfig struct {
	maxOpen    int
	maxIdle    int
	hasMaxOpen bool
	hasMaxIdle bool
	openKey    string
	idleKey    string
}

// inheriting fills the limits this scope left unset from the global pool. The
// key that supplies an inherited value comes along with it, so an error can
// name the global key rather than one that was never set.
func (p poolConfig) inheriting(global poolConfig) poolConfig {
	if !p.hasMaxOpen {
		p.maxOpen, p.hasMaxOpen, p.openKey = global.maxOpen, global.hasMaxOpen, global.openKey
	}
	if !p.hasMaxIdle {
		p.maxIdle, p.hasMaxIdle, p.idleKey = global.maxIdle, global.hasMaxIdle, global.idleKey
	}
	return p
}

// validatePool checks the effective limits of one database after inheritance.
// database/sql silently clamps IdleConns down to MaxOpenConns, so a pool whose
// idle limit is larger is a configuration mistake that would otherwise only
// show up as unexpected connection churn.
func validatePool(databaseID string, pool poolConfig) error {
	if pool.maxIdle <= pool.maxOpen {
		return nil
	}
	return fmt.Errorf("database %q: max idle connections %d (%s) exceeds max open connections %d (%s)",
		databaseID, pool.maxIdle, poolKeyName(pool.idleKey), pool.maxOpen, poolKeyName(pool.openKey))
}

func poolKeyName(key string) string {
	if key == "" {
		return "default"
	}
	return key
}

// configResolver reads environment settings, falling back to the pre-rename
// keys and recording a deprecation warning for each alias it uses.
//
// Blank values count as unset. Errors name the conflicting keys but never the
// values, because values can contain credentials. Warnings are ordered by the
// order settings are resolved, which is deterministic.
type configResolver struct {
	env      map[string]string
	settings []databaseSetting
	warnings []string
}

func newConfigResolver(env map[string]string) *configResolver {
	return &configResolver{env: env, settings: indexDatabaseSettings(env)}
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
// DATABASE_{ID}_{suffix} alias for the normalized id. On a conflict the current
// key wins with a warning: a display name or memo has no security impact, so
// failing startup over it would be disproportionate. rawID spells the keys
// named in messages when a key is not set. A memo is normalized to one line and
// capped, with a warning naming the key that supplied it.
func (r *configResolver) resolveDisplaySetting(id, rawID, suffix string) (string, error) {
	currentKey, legacyKey, err := r.settingKeys(id, rawID, suffix)
	if err != nil {
		return "", err
	}
	current := r.trim(currentKey)
	legacy := r.trim(legacyKey)

	var value, key string
	switch {
	case legacy == "":
		value, key = current, currentKey
	case current == "":
		r.warn("%s is deprecated; use %s instead", legacyKey, currentKey)
		value, key = legacy, legacyKey
	case current == legacy:
		r.warn("%s and %s are both set to the same value; %s is deprecated, use %s instead", legacyKey, currentKey, legacyKey, currentKey)
		value, key = current, currentKey
	default:
		r.warn("%s and %s are both set to different values; using %s", legacyKey, currentKey, currentKey)
		value, key = current, currentKey
	}

	if suffix == settingMemo {
		value = r.sanitizeMemoSetting(key, value)
	}
	return value, nil
}

// maxMemoRunes bounds a memo, which is rendered inside every tool description.
const maxMemoRunes = 200

// sanitizeMemoSetting normalizes a memo and warns about what it changed. The
// warning names the key that supplied the value, which may be a legacy alias.
func (r *configResolver) sanitizeMemoSetting(key, value string) string {
	if value == "" {
		return value
	}
	memo, normalized, truncated := sanitizeMemo(value)
	if normalized {
		r.warn("%s was normalized to a single line", key)
	}
	if truncated {
		r.warn("%s was truncated to %d characters", key, maxMemoRunes)
	}
	return memo
}

// sanitizeMemo turns a memo into printable single-line text: control characters
// and runs of whitespace become one space, and anything over maxMemoRunes is cut
// with an ellipsis. The cut counts runes, so multi-byte text such as CJK keeps
// its length and is never split mid-character.
func sanitizeMemo(value string) (memo string, normalized, truncated bool) {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	memo = strings.Join(strings.Fields(cleaned), " ")
	normalized = memo != strings.TrimSpace(value)

	runes := []rune(memo)
	if len(runes) > maxMemoRunes {
		memo = string(runes[:maxMemoRunes-1]) + "…"
		truncated = true
	}
	return memo, normalized, truncated
}

// parsePoolConfig resolves the pool settings for one database. An empty id
// means the global DB_MAX_* settings; otherwise the DB_{ID}_MAX_* keys are
// matched to the normalized id case-insensitively, and rawID spells the keys
// named in messages when a key is not set. Errors name the keys actually in
// use and never carry values.
func (r *configResolver) parsePoolConfig(id, rawID string) (poolConfig, error) {
	openKey, legacyOpenKey := envDatabaseMaxOpenConns, legacyEnvDatabaseMaxOpenConns
	idleKey, legacyIdleKey := envDatabaseMaxIdleConns, legacyEnvDatabaseMaxIdleConns
	if id != "" {
		var err error
		if openKey, legacyOpenKey, err = r.settingKeys(id, rawID, settingMaxOpenConns); err != nil {
			return poolConfig{}, err
		}
		if idleKey, legacyIdleKey, err = r.settingKeys(id, rawID, settingMaxIdleConns); err != nil {
			return poolConfig{}, err
		}
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
		pool.openKey = r.activeKey(openKey, legacyOpenKey)
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
		pool.idleKey = r.activeKey(idleKey, legacyIdleKey)
	}

	// The pair is validated after inheritance, by validatePool: a per-id idle
	// limit may legitimately exceed the built-in open limit when the global
	// settings raise it.
	return pool, nil
}

// settingKeys returns the current and legacy keys that set suffix for the
// normalized id. A side that is not set gets the spelling of the side that is,
// or of rawID, so resolve and activeKey see it as unset while messages still
// name a key the operator can set.
func (r *configResolver) settingKeys(id, rawID, suffix string) (currentKey, legacyKey string, err error) {
	current, err := r.pickSetting(id, suffix, false)
	if err != nil {
		return "", "", err
	}
	legacy, err := r.pickSetting(id, suffix, true)
	if err != nil {
		return "", "", err
	}

	if current.key != "" {
		rawID = current.rawID
	} else if legacy.key != "" {
		rawID = legacy.rawID
	}
	currentKey = fmt.Sprintf("DB_%s_%s", rawID, suffix)
	legacyKey = fmt.Sprintf("DATABASE_%s_%s", rawID, suffix)
	if current.key != "" {
		currentKey = current.key
	}
	if legacy.key != "" {
		legacyKey = legacy.key
	}
	return currentKey, legacyKey, nil
}

// pickSetting returns the one key that sets suffix for the normalized id with
// the given prefix. Ids are case-insensitive, so DB_APP_NAME and DB_app_NAME
// are spellings of the same setting: equal values are accepted with a warning,
// and different values are an error because neither spelling outranks the
// other.
func (r *configResolver) pickSetting(id, suffix string, legacy bool) (databaseSetting, error) {
	var picked databaseSetting
	for _, setting := range r.settings {
		if setting.id != id || setting.suffix != suffix || setting.legacy != legacy || setting.value == "" {
			continue
		}
		switch {
		case picked.key == "":
			picked = setting
		case picked.value != setting.value:
			return databaseSetting{}, fmt.Errorf("%s and %s are both set to different values; database ids are case-insensitive, so keep only one", picked.key, setting.key)
		default:
			r.warnDuplicateSpelling(picked.key, setting.key)
		}
	}
	return picked, nil
}

func (r *configResolver) warnDuplicateSpelling(firstKey, secondKey string) {
	r.warn("%s and %s are both set to the same value; database ids are case-insensitive, so keep only one", firstKey, secondKey)
}

// warnUnusedDatabaseSettings reports per-database keys that configure nothing:
// settings for an id that has no URL, and DB_DEFAULT_NAME / DB_DEFAULT_MEMO,
// which look like settings for the default database but never were. Without
// the warning such a typo or leftover is silently ignored.
func (r *configResolver) warnUnusedDatabaseSettings(named map[string]databaseURLSource, hasDefault bool) {
	for _, setting := range r.settings {
		if setting.suffix == settingURL || setting.value == "" {
			continue
		}
		if setting.id == defaultDatabaseID {
			switch {
			case setting.suffix == settingName:
				r.warn("%s does not apply to the default database; use %s instead", setting.key, envDatabaseName)
			case setting.suffix == settingMemo:
				r.warn("%s does not apply to the default database; use %s instead", setting.key, envDatabaseMemo)
			case !hasDefault:
				r.warn("%s has no effect because %s is not set", setting.key, envDatabaseURL)
			}
			continue
		}
		if _, configured := named[setting.id]; !configured {
			r.warn("%s has no effect because DB_%s_URL is not set", setting.key, setting.rawID)
		}
	}
}

// databaseURLSource is one resolved DB_{ID}_URL / DATABASE_{ID}_URL setting.
// id is the normalized routing id; rawID keeps the spelling used in the
// environment for the keys suggested in messages.
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
// normalized for grouping, so DB_APP_URL and DB_app_URL are the same database:
// equal values warn, different values are an error.
func discoverDatabaseURLs(r *configResolver) (map[string]databaseURLSource, error) {
	current := map[string]databaseURLSource{}
	legacy := map[string]databaseURLSource{}

	for _, setting := range r.settings {
		if setting.suffix != settingURL {
			continue
		}
		if setting.idErr != nil {
			return nil, fmt.Errorf("invalid database env %s: %w", setting.key, setting.idErr)
		}
		if setting.id == defaultDatabaseID {
			return nil, fmt.Errorf("%s is reserved; use %s for the default database", setting.key, envDatabaseURL)
		}
		if setting.value == "" {
			continue
		}

		source := databaseURLSource{id: setting.id, rawID: setting.rawID, key: setting.key, dsn: setting.value}
		group := current
		if setting.legacy {
			group = legacy
		}
		if existing, exists := group[setting.id]; exists {
			if existing.dsn != source.dsn {
				return nil, fmt.Errorf("database id %q is configured more than once by %s and %s", setting.id, existing.key, setting.key)
			}
			r.warnDuplicateSpelling(existing.key, setting.key)
			continue
		}
		group[setting.id] = source
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
