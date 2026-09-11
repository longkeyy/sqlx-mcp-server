package config

import "testing"

func TestParseConfigUsesDBURLAsDSN(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		envDatabaseURL: "sqlite:///tmp/example.db",
		envListen:      ":8090",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.DSN != "sqlite:///tmp/example.db" {
		t.Fatalf("expected DSN from DB_URL, got %q", config.DSN)
	}
	if config.Listen != ":8090" {
		t.Fatalf("expected listen address from LISTEN, got %q", config.Listen)
	}
	if config.DefaultDatabase != defaultDatabaseID {
		t.Fatalf("expected default database id, got %q", config.DefaultDatabase)
	}
	if len(config.Databases) != 1 || config.Databases[0].ID != defaultDatabaseID {
		t.Fatalf("unexpected databases: %+v", config.Databases)
	}
}

func TestParseConfigUsesDBNameAsDefaultInstanceName(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		envDatabaseURL:  "sqlite://:memory:",
		envDatabaseName: "app-db",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.Name != "app-db" {
		t.Fatalf("expected instance name from DB_NAME, got %q", config.Name)
	}
	if config.DSN != "sqlite://:memory:" {
		t.Fatalf("expected DSN to remain independent from instance name, got %q", config.DSN)
	}
	if config.Databases[0].Name != "app-db" {
		t.Fatalf("expected database display name from DB_NAME, got %q", config.Databases[0].Name)
	}
}

func TestParseConfigLoadsDefaultDatabaseMemo(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		envDatabaseURL:  "sqlite://:memory:",
		envDatabaseMemo: "temporary sqlite database for smoke tests",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.Databases[0].Memo != "temporary sqlite database for smoke tests" {
		t.Fatalf("expected default database memo, got %q", config.Databases[0].Memo)
	}
}

func TestParseConfigUsesCLIListen(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		envDatabaseURL: "sqlite://:memory:",
		envListen:      ":8088",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Listen != ":8088" {
		t.Fatalf("expected listen address from env, got %q", config.Listen)
	}

	config, err = parseConfig([]string{"--listen", "127.0.0.1:18088"}, map[string]string{
		envDatabaseURL: "sqlite://:memory:",
		envListen:      ":8088",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Listen != "127.0.0.1:18088" {
		t.Fatalf("expected CLI listen address to win, got %q", config.Listen)
	}
}

func TestParseConfigUsesAuthToken(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		envDatabaseURL: "sqlite://:memory:",
		envAuthToken:   "env-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.AuthToken != "env-token" {
		t.Fatalf("expected auth token from env, got %q", config.AuthToken)
	}

	config, err = parseConfig([]string{"--auth-token", "cli-token"}, map[string]string{
		envDatabaseURL: "sqlite://:memory:",
		envAuthToken:   "env-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.AuthToken != "cli-token" {
		t.Fatalf("expected CLI auth token to win, got %q", config.AuthToken)
	}
}

func TestParseConfigLoadsNamedDatabases(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":            "sqlite:///tmp/app.db",
		"DB_APP_NAME":           "app-db",
		"DB_APP_MEMO":           "primary application database",
		"DB_APP_MAX_OPEN_CONNS": "8",
		"DB_APP_MAX_IDLE_CONNS": "2",
		"DB_REPORTING_URL":      "sqlite:///tmp/reporting.db",
		envDatabaseMaxOpenConns: "3",
		envDefaultDatabase:      "APP",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.DefaultDatabase != "app" {
		t.Fatalf("expected app default database, got %q", config.DefaultDatabase)
	}
	if len(config.Databases) != 2 {
		t.Fatalf("expected two databases, got %+v", config.Databases)
	}
	if config.Databases[0].ID != "app" || config.Databases[1].ID != "reporting" {
		t.Fatalf("expected sorted database ids, got %+v", config.Databases)
	}
	if config.Databases[0].Name != "app-db" {
		t.Fatalf("expected optional database name, got %+v", config.Databases[0])
	}
	if config.Databases[0].Memo != "primary application database" {
		t.Fatalf("expected optional database memo, got %+v", config.Databases[0])
	}
	if config.Databases[1].MaxOpenConns != 3 || config.Databases[1].MaxIdleConns != defaultMaxIdleConns {
		t.Fatalf("expected reporting to use global pool defaults, got %+v", config.Databases[1])
	}
	if config.Databases[0].MaxOpenConns != 8 || config.Databases[0].MaxIdleConns != 2 {
		t.Fatalf("expected app to use per-database pool settings, got %+v", config.Databases[0])
	}
}

func TestParseConfigRejectsInvalidDatabaseID(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_BAD.ID_URL": "sqlite://:memory:",
	})
	if err == nil {
		t.Fatal("expected invalid database id error")
	}
}

func TestParseConfigRejectsUnknownDefaultDatabase(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":       "sqlite:///tmp/app.db",
		envDefaultDatabase: "missing",
	})
	if err == nil {
		t.Fatal("expected unknown default database error")
	}
}

func TestParseConfigRejectsInvalidPoolSettings(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":            "sqlite:///tmp/app.db",
		"DB_APP_MAX_OPEN_CONNS": "1",
		"DB_APP_MAX_IDLE_CONNS": "2",
	})
	if err == nil {
		t.Fatal("expected invalid pool settings error")
	}
}

func TestParseConfigRejectsDuplicateNormalizedDatabaseID(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_APP_URL": "sqlite:///tmp/app.db",
		"DB_app_URL": "sqlite:///tmp/app2.db",
	})
	if err == nil {
		t.Fatal("expected duplicate database id error")
	}
}
