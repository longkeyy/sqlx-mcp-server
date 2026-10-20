package config

import (
	"strings"
	"testing"
)

// The legacy alias keys are spelled out literally here on purpose: the tests
// must keep pinning the old names even if the implementation constants change.

func assertWarning(t *testing.T, warnings []string, substr string) {
	t.Helper()
	for _, warning := range warnings {
		if strings.Contains(warning, substr) {
			return
		}
	}
	t.Fatalf("expected a warning containing %q, got %q", substr, warnings)
}

func assertErrorMentions(t *testing.T, err error, substrs ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error mentioning %q", substrs)
	}
	for _, substr := range substrs {
		if !strings.Contains(err.Error(), substr) {
			t.Fatalf("expected error to mention %q, got %q", substr, err)
		}
	}
}

func TestParseConfigAcceptsLegacyDatabaseURL(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DATABASE_URL": "sqlite:///tmp/legacy.db",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.DSN != "sqlite:///tmp/legacy.db" {
		t.Fatalf("expected DSN from DATABASE_URL, got %q", config.DSN)
	}
	if config.DefaultDatabase != defaultDatabaseID {
		t.Fatalf("expected legacy DSN to become the default database, got %q", config.DefaultDatabase)
	}
	assertWarning(t, config.Warnings, "DATABASE_URL is deprecated; use DB_URL")
}

func TestParseConfigWarnsWhenDatabaseURLAliasMatches(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_URL":       "sqlite:///tmp/app.db",
		"DATABASE_URL": "sqlite:///tmp/app.db",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.DSN != "sqlite:///tmp/app.db" {
		t.Fatalf("expected DSN from matching aliases, got %q", config.DSN)
	}
	assertWarning(t, config.Warnings, "both set to the same value")
}

func TestParseConfigRejectsConflictingDatabaseURLAliases(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_URL":       "sqlite:///tmp/current.db",
		"DATABASE_URL": "sqlite:///tmp/legacy.db",
	})
	assertErrorMentions(t, err, "DB_URL", "DATABASE_URL")
	for _, value := range []string{"current.db", "legacy.db"} {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("error must not include DSN values, got %q", err)
		}
	}
}

func TestParseConfigAcceptsLegacyNamedDatabases(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DATABASE_APP_URL":            "sqlite:///tmp/app.db",
		"DATABASE_APP_NAME":           "app-db",
		"DATABASE_APP_MAX_OPEN_CONNS": "8",
		"DATABASE_APP_MAX_IDLE_CONNS": "2",
		"DATABASE_REPORTING_URL":      "sqlite:///tmp/reporting.db",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(config.Databases) != 2 {
		t.Fatalf("expected two databases, got %+v", config.Databases)
	}
	if config.Databases[0].ID != "app" || config.Databases[1].ID != "reporting" {
		t.Fatalf("expected sorted app and reporting ids, got %+v", config.Databases)
	}
	if config.Databases[0].Name != "app-db" {
		t.Fatalf("expected name from DATABASE_APP_NAME, got %q", config.Databases[0].Name)
	}
	if config.Databases[0].MaxOpenConns != 8 || config.Databases[0].MaxIdleConns != 2 {
		t.Fatalf("expected pool settings from DATABASE_APP_MAX_*, got %+v", config.Databases[0])
	}
	if config.Databases[1].MaxOpenConns != defaultMaxOpenConns || config.Databases[1].MaxIdleConns != defaultMaxIdleConns {
		t.Fatalf("expected built-in pool defaults, got %+v", config.Databases[1])
	}
	if config.DefaultDatabase != "app" {
		t.Fatalf("expected first sorted id as default, got %q", config.DefaultDatabase)
	}
	assertWarning(t, config.Warnings, "DATABASE_APP_URL is deprecated; use DB_APP_URL")
	assertWarning(t, config.Warnings, `DEFAULT_DB is not set; using "app" as the default database`)
}

func TestParseConfigUsesInstanceNameOverDBName(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_URL":        "sqlite://:memory:",
		"INSTANCE_NAME": "app-db",
		"DB_NAME":       "new-display-name",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.Name != "app-db" {
		t.Fatalf("expected instance name to win, got %q", config.Name)
	}
	if config.Databases[0].Name != "app-db" {
		t.Fatalf("expected database display name from INSTANCE_NAME, got %q", config.Databases[0].Name)
	}
	assertWarning(t, config.Warnings, "INSTANCE_NAME is deprecated")
}

func TestParseConfigUsesCLINameOverInstanceName(t *testing.T) {
	config, err := parseConfig([]string{"--name", "cli-name"}, map[string]string{
		"DB_URL":        "sqlite://:memory:",
		"INSTANCE_NAME": "app-db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Name != "cli-name" {
		t.Fatalf("expected CLI name to win, got %q", config.Name)
	}
}

func TestParseConfigKeepsDBNameAsLegacyInstanceName(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_URL":  "sqlite://:memory:",
		"DB_NAME": "legacy-display-name",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.Name != "legacy-display-name" {
		t.Fatalf("expected DB_NAME to set the instance name, got %q", config.Name)
	}
	if config.Databases[0].Name != "legacy-display-name" {
		t.Fatalf("expected DB_NAME to set the database display name, got %q", config.Databases[0].Name)
	}
	if len(config.Warnings) != 0 {
		t.Fatalf("expected no warnings for current keys, got %q", config.Warnings)
	}
}

func TestParseConfigAcceptsLegacyDefaultDatabase(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":       "sqlite:///tmp/app.db",
		"DB_REPORTING_URL": "sqlite:///tmp/reporting.db",
		"DEFAULT_DATABASE": "APP",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.DefaultDatabase != "app" {
		t.Fatalf("expected default from DEFAULT_DATABASE, got %q", config.DefaultDatabase)
	}
	assertWarning(t, config.Warnings, "DEFAULT_DATABASE is deprecated; use DEFAULT_DB")
}

func TestParseConfigAcceptsMatchingDefaultDatabaseAliases(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":       "sqlite:///tmp/app.db",
		"DB_REPORTING_URL": "sqlite:///tmp/reporting.db",
		"DEFAULT_DB":       "APP",
		"DEFAULT_DATABASE": "app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.DefaultDatabase != "app" {
		t.Fatalf("expected normalized default, got %q", config.DefaultDatabase)
	}
	assertWarning(t, config.Warnings, "both set to the same value")
}

func TestParseConfigRejectsConflictingDefaultDatabaseAliases(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":       "sqlite:///tmp/app.db",
		"DB_REPORTING_URL": "sqlite:///tmp/reporting.db",
		"DEFAULT_DB":       "app",
		"DEFAULT_DATABASE": "reporting",
	})
	assertErrorMentions(t, err, "DEFAULT_DB", "DEFAULT_DATABASE")
}

func TestParseConfigAcceptsLegacyPoolSettings(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_URL":                  "sqlite:///tmp/app.db",
		"DATABASE_MAX_OPEN_CONNS": "7",
		"DATABASE_MAX_IDLE_CONNS": "2",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.Databases[0].MaxOpenConns != 7 || config.Databases[0].MaxIdleConns != 2 {
		t.Fatalf("expected pool settings from DATABASE_MAX_*, got %+v", config.Databases[0])
	}
	assertWarning(t, config.Warnings, "DATABASE_MAX_OPEN_CONNS is deprecated; use DB_MAX_OPEN_CONNS")
}

func TestParseConfigRejectsConflictingPoolAliases(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_URL":                  "sqlite:///tmp/app.db",
		"DB_MAX_OPEN_CONNS":       "5",
		"DATABASE_MAX_OPEN_CONNS": "6",
	})
	assertErrorMentions(t, err, "DB_MAX_OPEN_CONNS", "DATABASE_MAX_OPEN_CONNS")
}

func TestParseConfigRejectsConflictingNamedDatabaseURLAliases(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":       "sqlite:///tmp/app.db",
		"DATABASE_APP_URL": "sqlite:///tmp/other.db",
	})
	assertErrorMentions(t, err, "DB_APP_URL", "DATABASE_APP_URL")
	if strings.Contains(err.Error(), "other.db") {
		t.Fatalf("error must not include DSN values, got %q", err)
	}
}

func TestParseConfigAcceptsMatchingNamedDatabaseURLAliases(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":       "sqlite:///tmp/app.db",
		"DATABASE_APP_URL": "sqlite:///tmp/app.db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Databases) != 1 || config.Databases[0].ID != "app" {
		t.Fatalf("expected one app database, got %+v", config.Databases)
	}
	assertWarning(t, config.Warnings, "both set to the same value")
}

func TestParseConfigTreatsBlankCurrentKeysAsUnset(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_URL":        "",
		"DATABASE_URL":  "sqlite:///tmp/legacy.db",
		"DB_NAME":       "   ",
		"INSTANCE_NAME": "demo",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.DSN != "sqlite:///tmp/legacy.db" {
		t.Fatalf("expected blank DB_URL to fall through to DATABASE_URL, got %q", config.DSN)
	}
	if config.Name != "demo" {
		t.Fatalf("expected blank DB_NAME to fall through to INSTANCE_NAME, got %q", config.Name)
	}
}
