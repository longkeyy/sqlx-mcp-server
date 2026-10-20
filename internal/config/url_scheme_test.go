package config

import (
	"testing"
)

// A DB_{ID}_URL set to an http(s) address is almost always the endpoint of
// something else, such as an API or a health check, that happens to share the
// DB_* prefix. Startup cannot use it, so it is skipped with a warning instead
// of failing every other configured database.

func TestParseConfigSkipsHTTPPerIDURL(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL": "sqlite:///tmp/app.db",
		"DB_API_URL": "https://api.example.com/health",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Databases) != 1 || config.Databases[0].ID != "app" {
		t.Fatalf("expected only the app database, got %+v", config.Databases)
	}
	assertWarning(t, config.Warnings, "DB_API_URL is not a database URL and is ignored")
}

func TestParseConfigSkipsHTTPSLegacyPerIDURL(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":           "sqlite:///tmp/app.db",
		"DATABASE_WEBHOOK_URL": "http://example.com/hook",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Databases) != 1 {
		t.Fatalf("expected only the app database, got %+v", config.Databases)
	}
	assertWarning(t, config.Warnings, "DATABASE_WEBHOOK_URL is not a database URL and is ignored")
}

func TestParseConfigSkipsHTTPPerIDURLCaseInsensitively(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL": "sqlite:///tmp/app.db",
		"DB_API_URL": "HTTPS://api.example.com/health",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Databases) != 1 {
		t.Fatalf("expected only the app database, got %+v", config.Databases)
	}
	assertWarning(t, config.Warnings, "DB_API_URL is not a database URL and is ignored")
}

func TestParseConfigSkipsHTTPPerIDURLWithoutWarningAboutItsSettings(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":  "sqlite:///tmp/app.db",
		"DB_API_URL":  "https://api.example.com/health",
		"DB_API_NAME": "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Databases) != 1 {
		t.Fatalf("expected only the app database, got %+v", config.Databases)
	}
	// One warning for the skipped URL, and none claiming DB_API_NAME has no
	// effect because DB_API_URL is not set: the operator did set it.
	if len(config.Warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %q", config.Warnings)
	}
}

// Only http(s) is filtered. Any other scheme still reaches the DSN parser,
// which rejects it when the server starts, so a typo is never silently dropped.

func TestParseConfigKeepsUnsupportedPerIDScheme(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL": "ftp://example.com/app.db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Databases) != 1 || config.Databases[0].DSN != "ftp://example.com/app.db" {
		t.Fatalf("expected the unsupported URL to reach startup, got %+v", config.Databases)
	}
	if len(config.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %q", config.Warnings)
	}
}

// Command-line values come from a shell where quoting often leaves padding, so
// the flags are trimmed like the environment values they mirror.

func TestParseConfigTrimsFlagDSNAndName(t *testing.T) {
	config, err := parseConfig([]string{
		"--dsn", "  sqlite:///tmp/app.db  ",
		"--name", "  trimmed-name  ",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if config.DSN != "sqlite:///tmp/app.db" {
		t.Fatalf("expected a trimmed DSN, got %q", config.DSN)
	}
	if config.Name != "trimmed-name" {
		t.Fatalf("expected a trimmed name, got %q", config.Name)
	}
}

func TestParseConfigTrimsEnvironmentDSNAndName(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_URL":  "  sqlite:///tmp/app.db\n",
		"DB_NAME": "\tdemo-name ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.DSN != "sqlite:///tmp/app.db" {
		t.Fatalf("expected a trimmed DSN, got %q", config.DSN)
	}
	if config.Name != "demo-name" {
		t.Fatalf("expected a trimmed name, got %q", config.Name)
	}
}
