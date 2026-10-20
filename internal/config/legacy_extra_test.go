package config

import (
	"os"
	"strings"
	"testing"
)

// These cases pin the alias-resolution rules that the main legacy tests do not
// reach: which key wins, which key is named in an error, and when to stay quiet.

func TestParseConfigCLIFlagSkipsLegacyDSNResolution(t *testing.T) {
	config, err := parseConfig([]string{"--dsn", "sqlite://:memory:"}, map[string]string{
		"DATABASE_URL": "sqlite:///tmp/legacy.db",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.DSN != "sqlite://:memory:" {
		t.Fatalf("expected the --dsn value to win, got %q", config.DSN)
	}
	for _, warning := range config.Warnings {
		if strings.Contains(warning, "DATABASE_URL") {
			t.Fatalf("expected no warning for an unused DATABASE_URL, got %q", config.Warnings)
		}
	}
}

func TestParseConfigWarnsWhenPerIDDisplaySettingsConflict(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":        "sqlite:///tmp/app.db",
		"DB_APP_NAME":       "new-name",
		"DATABASE_APP_NAME": "old-name",
		"DB_APP_MEMO":       "new memo",
		"DATABASE_APP_MEMO": "old memo",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.Databases[0].Name != "new-name" {
		t.Fatalf("expected DB_APP_NAME to win, got %q", config.Databases[0].Name)
	}
	if config.Databases[0].Memo != "new memo" {
		t.Fatalf("expected DB_APP_MEMO to win, got %q", config.Databases[0].Memo)
	}
	assertWarning(t, config.Warnings, "using DB_APP_NAME")
	assertWarning(t, config.Warnings, "using DB_APP_MEMO")
}

func TestParseConfigReportsLegacyPoolKeyOnInvalidValue(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DATABASE_APP_URL":            "sqlite:///tmp/app.db",
		"DATABASE_APP_MAX_OPEN_CONNS": "abc",
	})
	assertErrorMentions(t, err, "DATABASE_APP_MAX_OPEN_CONNS")
	if strings.Contains(err.Error(), "abc") {
		t.Fatalf("error must not echo the invalid value, got %q", err)
	}
}

func TestParseConfigRejectsReservedLegacyDefaultDatabaseURL(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DATABASE_DEFAULT_URL": "sqlite:///tmp/reserved.db",
	})
	assertErrorMentions(t, err, "DATABASE_DEFAULT_URL", "DB_URL")
	if strings.Contains(err.Error(), "reserved.db") {
		t.Fatalf("error must not include the DSN, got %q", err)
	}
}

func TestParseConfigDoesNotWarnForSingleImplicitDefault(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL": "sqlite:///tmp/app.db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.DefaultDatabase != "app" {
		t.Fatalf("expected the only database to become the default, got %q", config.DefaultDatabase)
	}
	if len(config.Warnings) != 0 {
		t.Fatalf("expected no warnings for a single implicit default, got %q", config.Warnings)
	}
}

func TestParseConfigWarnsWhenInstanceNameMatchesDBName(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_URL":        "sqlite://:memory:",
		"INSTANCE_NAME": "same-name",
		"DB_NAME":       "same-name",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Name != "same-name" {
		t.Fatalf("expected the shared name, got %q", config.Name)
	}
	assertWarning(t, config.Warnings, "INSTANCE_NAME and DB_NAME are both set to the same value")
}

func TestParseConfigTrimsLegacyValues(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DATABASE_URL":  "  sqlite:///tmp/legacy.db  ",
		"INSTANCE_NAME": "  demo  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.DSN != "sqlite:///tmp/legacy.db" {
		t.Fatalf("expected trimmed DSN, got %q", config.DSN)
	}
	if config.Name != "demo" {
		t.Fatalf("expected trimmed instance name, got %q", config.Name)
	}
}

func TestParseConfigDuplicateDatabaseIDErrorNamesKeysNotDSNs(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"current": {
			"DB_APP_URL": "sqlite:///tmp/app.db",
			"DB_app_URL": "sqlite:///tmp/app2.db",
		},
		"legacy": {
			"DATABASE_APP_URL": "sqlite:///tmp/app.db",
			"DATABASE_app_URL": "sqlite:///tmp/app2.db",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseConfig(nil, env)
			for key := range env {
				assertErrorMentions(t, err, key)
			}
			for _, value := range []string{"/tmp/app.db", "/tmp/app2.db"} {
				if strings.Contains(err.Error(), value) {
					t.Fatalf("error must not include DSN values, got %q", err)
				}
			}
		})
	}
}

func TestParseConfigRequiresADatabaseWhenAllURLsAreBlank(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_URL":           "   ",
		"DATABASE_URL":     "",
		"DATABASE_APP_URL": " ",
	})
	assertErrorMentions(t, err, "DSN is required")
}

func TestParseConfigNamesLegacyDefaultDatabaseKeyInErrors(t *testing.T) {
	env := map[string]string{"DB_APP_URL": "sqlite:///tmp/app.db"}

	env["DEFAULT_DATABASE"] = "bad.id"
	_, err := parseConfig(nil, env)
	assertErrorMentions(t, err, "invalid DEFAULT_DATABASE")

	env["DEFAULT_DATABASE"] = "missing"
	_, err = parseConfig(nil, env)
	assertErrorMentions(t, err, `DEFAULT_DATABASE "missing" does not match any configured database`)
}

func TestParseConfigRejectsConflictingIdlePoolAliases(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_URL":                  "sqlite:///tmp/app.db",
		"DB_MAX_IDLE_CONNS":       "1",
		"DATABASE_MAX_IDLE_CONNS": "2",
	})
	assertErrorMentions(t, err, "DB_MAX_IDLE_CONNS", "DATABASE_MAX_IDLE_CONNS")
}

func TestParseConfigNamesLegacyPoolKeysInRangeErrors(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_URL":                  "sqlite:///tmp/app.db",
		"DATABASE_MAX_IDLE_CONNS": "0",
	})
	assertErrorMentions(t, err, "DATABASE_MAX_IDLE_CONNS must be a positive integer")

	_, err = parseConfig(nil, map[string]string{
		"DATABASE_APP_URL":            "sqlite:///tmp/app.db",
		"DATABASE_APP_MAX_OPEN_CONNS": "1",
		"DATABASE_APP_MAX_IDLE_CONNS": "2",
	})
	assertErrorMentions(t, err, "DATABASE_APP_MAX_IDLE_CONNS must be less than or equal to DATABASE_APP_MAX_OPEN_CONNS")
}

func TestParseConfigRejectsUnknownFlag(t *testing.T) {
	if _, err := parseConfig([]string{"--no-such-flag"}, nil); err == nil {
		t.Fatal("expected an unknown flag error")
	}
}

func TestParseReadsLegacyAliasesFromProcessEnvironment(t *testing.T) {
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })
	os.Args = []string{"sql-mcp"}

	t.Setenv("DB_URL", "")
	t.Setenv("DATABASE_URL", "sqlite:///tmp/process-env.db")
	t.Setenv("DEFAULT_DB", "")
	t.Setenv("DEFAULT_DATABASE", "")

	config, err := Parse()
	if err != nil {
		t.Fatal(err)
	}
	if config.DSN != "sqlite:///tmp/process-env.db" {
		t.Fatalf("expected DSN from the process environment, got %q", config.DSN)
	}
	assertWarning(t, config.Warnings, "DATABASE_URL is deprecated; use DB_URL")
}

func TestParseConfigWarningsNeverContainValues(t *testing.T) {
	const secret = "s3cr3t-pa55"
	config, err := parseConfig(nil, map[string]string{
		"DATABASE_URL":            "postgres://alice:" + secret + "@db.internal:5432/app",
		"DATABASE_REPORTING_URL":  "postgres://bob:" + secret + "@db.internal:5432/reporting",
		"DB_REPORTING_URL":        "postgres://bob:" + secret + "@db.internal:5432/reporting",
		"DATABASE_MAX_OPEN_CONNS": "9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Warnings) == 0 {
		t.Fatal("expected deprecation warnings")
	}
	for _, warning := range config.Warnings {
		for _, fragment := range []string{secret, "db.internal", "alice", "bob"} {
			if strings.Contains(warning, fragment) {
				t.Fatalf("warning %q leaks %q", warning, fragment)
			}
		}
	}
}
