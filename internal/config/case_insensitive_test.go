package config

import (
	"strings"
	"testing"
)

// The id in DB_{ID}_* has always been normalized for routing, but the NAME,
// MEMO and pool lookups used to rebuild keys from the URL key's spelling, so
// DB_APP_URL and DB_app_NAME did not belong to the same database. These cases
// pin the indexed lookup that matches every setting for one id.

func TestParseConfigMatchesPerIDSettingsCaseInsensitively(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":            "sqlite:///tmp/app.db",
		"DB_app_NAME":           "app-db",
		"DB_App_MEMO":           "primary application database",
		"DB_aPp_MAX_OPEN_CONNS": "8",
		"DB_APP_MAX_IDLE_CONNS": "2",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(config.Databases) != 1 {
		t.Fatalf("expected one database, got %+v", config.Databases)
	}
	database := config.Databases[0]
	if database.ID != "app" {
		t.Fatalf("expected normalized id app, got %q", database.ID)
	}
	if database.Name != "app-db" {
		t.Fatalf("expected DB_app_NAME to apply to app, got %q", database.Name)
	}
	if database.Memo != "primary application database" {
		t.Fatalf("expected DB_App_MEMO to apply to app, got %q", database.Memo)
	}
	if database.MaxOpenConns != 8 || database.MaxIdleConns != 2 {
		t.Fatalf("expected per-id pool settings to apply to app, got %+v", database)
	}
}

func TestParseConfigAcceptsEqualDuplicateURLCaseSpellings(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL": "sqlite:///tmp/app.db",
		"DB_app_URL": "sqlite:///tmp/app.db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Databases) != 1 || config.Databases[0].DSN != "sqlite:///tmp/app.db" {
		t.Fatalf("expected a single app database, got %+v", config.Databases)
	}
	assertWarning(t, config.Warnings, "DB_APP_URL and DB_app_URL are both set to the same value")
}

func TestParseConfigRejectsDifferingDuplicateNameSpellings(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":  "sqlite:///tmp/app.db",
		"DB_APP_NAME": "first-name",
		"DB_app_NAME": "second-name",
	})
	assertErrorMentions(t, err, "DB_APP_NAME", "DB_app_NAME")
	for _, value := range []string{"first-name", "second-name", "/tmp/app.db"} {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("error must not include values, got %q", err)
		}
	}
}

func TestParseConfigWarnsAboutSettingsWithoutADatabase(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":     "sqlite:///tmp/app.db",
		"DB_ORPHAN_NAME": "orphan-db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Databases) != 1 {
		t.Fatalf("expected the orphan settings to be ignored, got %+v", config.Databases)
	}
	assertWarning(t, config.Warnings, "DB_ORPHAN_NAME has no effect because DB_ORPHAN_URL is not set")
}

func TestParseConfigPointsDefaultDisplayKeysAtGlobalNames(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_URL":          "sqlite://:memory:",
		"DB_DEFAULT_NAME": "default-db",
		"DB_DEFAULT_MEMO": "default memo",
	})
	if err != nil {
		t.Fatal(err)
	}

	database := config.Databases[0]
	if database.Name != "sql-mcp-default" {
		t.Fatalf("expected DB_DEFAULT_NAME not to rename the default, got %q", database.Name)
	}
	if database.Memo != "" {
		t.Fatalf("expected DB_DEFAULT_MEMO not to set the memo, got %q", database.Memo)
	}
	assertWarning(t, config.Warnings, "DB_DEFAULT_NAME does not apply to the default database; use DB_NAME instead")
	assertWarning(t, config.Warnings, "DB_DEFAULT_MEMO does not apply to the default database; use DB_MEMO instead")
}

func TestParseConfigAppliesDefaultPoolSettings(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_URL":                    "sqlite://:memory:",
		"DB_DEFAULT_MAX_OPEN_CONNS": "5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Databases[0].MaxOpenConns != 5 {
		t.Fatalf("expected DB_DEFAULT_MAX_OPEN_CONNS to apply, got %+v", config.Databases[0])
	}
}

func TestParseConfigSuggestsCurrentSpellingForLegacyCaseVariant(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DATABASE_APP_URL":  "sqlite:///tmp/app.db",
		"DATABASE_app_NAME": "app-db",
	})
	if err != nil {
		t.Fatal(err)
	}
	database := config.Databases[0]
	if database.Name != "app-db" {
		t.Fatalf("expected the legacy name to apply, got %q", database.Name)
	}
	assertWarning(t, config.Warnings, "DATABASE_APP_URL is deprecated; use DB_APP_URL instead")
	assertWarning(t, config.Warnings, "DATABASE_app_NAME is deprecated; use DB_app_NAME instead")
}

func TestParseConfigRejectsDifferingDuplicateURLSpellings(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_APP_URL": "sqlite:///tmp/first.db",
		"DB_app_URL": "sqlite:///tmp/second.db",
	})
	assertErrorMentions(t, err, "DB_APP_URL", "DB_app_URL")
	for _, value := range []string{"first.db", "second.db"} {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("error must not include DSN values, got %q", err)
		}
	}
}

func TestParseConfigNamesLegacyPoolKeysForCaseVariantIDs(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":                  "sqlite:///tmp/app.db",
		"DATABASE_app_MAX_OPEN_CONNS": "1",
		"DATABASE_app_MAX_IDLE_CONNS": "2",
	})
	assertErrorMentions(t, err, "DATABASE_app_MAX_IDLE_CONNS must be less than or equal to DATABASE_app_MAX_OPEN_CONNS")
}

func TestParseConfigWarnsAboutPoolSettingsForDefaultWithoutURL(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":                "sqlite:///tmp/app.db",
		"DB_DEFAULT_MAX_OPEN_CONNS": "5",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertWarning(t, config.Warnings, "DB_DEFAULT_MAX_OPEN_CONNS has no effect because DB_URL is not set")
}
