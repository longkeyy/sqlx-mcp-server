package config

import "testing"

// With DB_URL, DB_NAME and DB_MEMO describe the default database. Without it
// the default is one of the named databases, so these cases pin when the
// global display settings fill that database and when they warn instead.

func TestParseConfigAppliesGlobalDisplayToSingleNamedDatabase(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL": "sqlite:///tmp/app.db",
		"DB_NAME":    "app-db",
		"DB_MEMO":    "primary application database",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.DefaultDatabase != "app" {
		t.Fatalf("expected the only database to become the default, got %q", config.DefaultDatabase)
	}

	database := config.Databases[0]
	if database.Name != "app-db" {
		t.Fatalf("expected DB_NAME to name the only database, got %q", database.Name)
	}
	if database.Memo != "primary application database" {
		t.Fatalf("expected DB_MEMO to describe the only database, got %q", database.Memo)
	}
	if config.Name != "app-db" {
		t.Fatalf("expected DB_NAME to keep naming the server, got %q", config.Name)
	}
	if len(config.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %q", config.Warnings)
	}
}

func TestParseConfigAppliesGlobalDisplayToExplicitDefault(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":       "sqlite:///tmp/app.db",
		"DB_REPORTING_URL": "sqlite:///tmp/reporting.db",
		"DB_NAME":          "reporting-db",
		"DB_MEMO":          "analytics and bi replica",
		"DEFAULT_DB":       "REPORTING",
	})
	if err != nil {
		t.Fatal(err)
	}

	reporting := databaseByID(config.Databases, "reporting")
	if reporting.Name != "reporting-db" || reporting.Memo != "analytics and bi replica" {
		t.Fatalf("expected DB_NAME and DB_MEMO on the explicit default, got %+v", reporting)
	}
	app := databaseByID(config.Databases, "app")
	if app.Name != "sql-mcp-app" || app.Memo != "" {
		t.Fatalf("expected the non-default database to keep its generated name, got %+v", app)
	}
	if len(config.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %q", config.Warnings)
	}
}

func TestParseConfigPrefersPerIDDisplayOnNamedDefault(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":  "sqlite:///tmp/app.db",
		"DB_APP_NAME": "per-id",
		"DB_APP_MEMO": "per-id memo",
		"DB_NAME":     "global-name",
		"DB_MEMO":     "global memo",
	})
	if err != nil {
		t.Fatal(err)
	}

	database := config.Databases[0]
	if database.Name != "per-id" || database.Memo != "per-id memo" {
		t.Fatalf("expected DB_APP_NAME and DB_APP_MEMO to win, got %+v", database)
	}
	if config.Name != "global-name" {
		t.Fatalf("expected DB_NAME to keep naming the server, got %q", config.Name)
	}
	if len(config.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %q", config.Warnings)
	}
}

func TestParseConfigWarnsWhenGlobalDisplayHasNoNamedDefault(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":       "sqlite:///tmp/app.db",
		"DB_REPORTING_URL": "sqlite:///tmp/reporting.db",
		"DB_NAME":          "server-name",
		"DB_MEMO":          "shared note",
	})
	if err != nil {
		t.Fatal(err)
	}

	app := databaseByID(config.Databases, "app")
	if app.Name != "sql-mcp-app" || app.Memo != "" {
		t.Fatalf("expected the implicit default to keep its generated display, got %+v", app)
	}
	if config.Name != "server-name" {
		t.Fatalf("expected DB_NAME to still name the server, got %q", config.Name)
	}
	assertWarning(t, config.Warnings, `DB_NAME is not applied to database "app" because DEFAULT_DB is not set; set DEFAULT_DB or DB_APP_NAME`)
	assertWarning(t, config.Warnings, `DB_MEMO is not applied to database "app" because DEFAULT_DB is not set; set DEFAULT_DB or DB_APP_MEMO`)
}

func TestParseConfigNamesCLIFlagWhenGlobalNameHasNoNamedDefault(t *testing.T) {
	config, err := parseConfig([]string{"--name", "cli-name"}, map[string]string{
		"DB_APP_URL":       "sqlite:///tmp/app.db",
		"DB_REPORTING_URL": "sqlite:///tmp/reporting.db",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertWarning(t, config.Warnings, `--name is not applied to database "app" because DEFAULT_DB is not set`)
}
