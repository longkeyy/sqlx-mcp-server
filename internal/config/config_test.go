package config

import "testing"

func TestParseConfigUsesDatabaseURLAsDSN(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		envDatabaseURL: "sqlite:///tmp/example.db",
		envServerMode:  "http",
		envServerPort:  "8090",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.DSN != "sqlite:///tmp/example.db" {
		t.Fatalf("expected DSN from DATABASE_URL, got %q", config.DSN)
	}
	if config.ServerMode != "http" {
		t.Fatalf("expected http mode, got %q", config.ServerMode)
	}
	if config.Port != 8090 {
		t.Fatalf("expected port 8090, got %d", config.Port)
	}
	if config.DefaultDatabase != defaultDatabaseID {
		t.Fatalf("expected default database id, got %q", config.DefaultDatabase)
	}
	if len(config.Databases) != 1 || config.Databases[0].ID != defaultDatabaseID {
		t.Fatalf("unexpected databases: %+v", config.Databases)
	}
}

func TestParseConfigUsesInstanceNameNotDatabaseName(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		envDatabaseURL:  "sqlite://:memory:",
		envInstanceName: "app-db",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.Name != "app-db" {
		t.Fatalf("expected instance name from INSTANCE_NAME, got %q", config.Name)
	}
	if config.DSN != "sqlite://:memory:" {
		t.Fatalf("expected DSN to remain independent from instance name, got %q", config.DSN)
	}
	if config.Databases[0].Name != "app-db" {
		t.Fatalf("expected database display name from INSTANCE_NAME, got %q", config.Databases[0].Name)
	}
}

func TestParseConfigKeepsDBNameAsLegacyInstanceName(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		envDatabaseURL:  "sqlite://:memory:",
		envLegacyDBName: "legacy-display-name",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.Name != "legacy-display-name" {
		t.Fatalf("expected legacy DB_NAME to populate instance name, got %q", config.Name)
	}
}

func TestParseConfigAcceptsAllServerMode(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		envDatabaseURL: "sqlite://:memory:",
		envServerMode:  "all",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.ServerMode != "all" {
		t.Fatalf("expected all mode, got %q", config.ServerMode)
	}
}

func TestParseConfigLoadsNamedDatabases(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DATABASE_APP_URL":            "sqlite:///tmp/app.db",
		"DATABASE_APP_NAME":           "app-db",
		"DATABASE_APP_MAX_OPEN_CONNS": "8",
		"DATABASE_APP_MAX_IDLE_CONNS": "2",
		"DATABASE_REPORTING_URL":      "sqlite:///tmp/reporting.db",
		envDatabaseMaxOpenConns:       "3",
		envDefaultDatabase:            "APP",
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
	if config.Databases[1].MaxOpenConns != 3 || config.Databases[1].MaxIdleConns != defaultMaxIdleConns {
		t.Fatalf("expected reporting to use global pool defaults, got %+v", config.Databases[1])
	}
	if config.Databases[0].MaxOpenConns != 8 || config.Databases[0].MaxIdleConns != 2 {
		t.Fatalf("expected app to use per-database pool settings, got %+v", config.Databases[0])
	}
}

func TestParseConfigRejectsInvalidDatabaseID(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DATABASE_BAD.ID_URL": "sqlite://:memory:",
	})
	if err == nil {
		t.Fatal("expected invalid database id error")
	}
}

func TestParseConfigRejectsUnknownDefaultDatabase(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DATABASE_APP_URL": "sqlite:///tmp/app.db",
		envDefaultDatabase: "missing",
	})
	if err == nil {
		t.Fatal("expected unknown default database error")
	}
}

func TestParseConfigRejectsInvalidPoolSettings(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DATABASE_APP_URL":            "sqlite:///tmp/app.db",
		"DATABASE_APP_MAX_OPEN_CONNS": "1",
		"DATABASE_APP_MAX_IDLE_CONNS": "2",
	})
	if err == nil {
		t.Fatal("expected invalid pool settings error")
	}
}

func TestParseConfigRejectsDuplicateNormalizedDatabaseID(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DATABASE_APP_URL": "sqlite:///tmp/app.db",
		"DATABASE_app_URL": "sqlite:///tmp/app2.db",
	})
	if err == nil {
		t.Fatal("expected duplicate database id error")
	}
}
