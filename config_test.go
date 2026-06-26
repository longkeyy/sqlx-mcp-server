package main

import "testing"

func TestParseConfigUsesDatabaseURLAsDSN(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		envDatabaseURL: "sqlite:///tmp/tcm_unified.db",
		envServerMode:  "http",
		envServerPort:  "8090",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.DSN != "sqlite:///tmp/tcm_unified.db" {
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
		envInstanceName: "tcm-db",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.Name != "tcm-db" {
		t.Fatalf("expected instance name from INSTANCE_NAME, got %q", config.Name)
	}
	if config.DSN != "sqlite://:memory:" {
		t.Fatalf("expected DSN to remain independent from instance name, got %q", config.DSN)
	}
	if config.Databases[0].Name != "tcm-db" {
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

func TestParseConfigLoadsNamedDatabases(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DATABASE_TCM_URL":            "sqlite:///tmp/tcm.db",
		"DATABASE_TCM_NAME":           "tcm-db",
		"DATABASE_TCM_MAX_OPEN_CONNS": "8",
		"DATABASE_TCM_MAX_IDLE_CONNS": "2",
		"DATABASE_HERB_URL":           "sqlite:///tmp/herb.db",
		envDatabaseMaxOpenConns:       "3",
		envDefaultDatabase:            "TCM",
	})
	if err != nil {
		t.Fatal(err)
	}

	if config.DefaultDatabase != "tcm" {
		t.Fatalf("expected tcm default database, got %q", config.DefaultDatabase)
	}
	if len(config.Databases) != 2 {
		t.Fatalf("expected two databases, got %+v", config.Databases)
	}
	if config.Databases[0].ID != "herb" || config.Databases[1].ID != "tcm" {
		t.Fatalf("expected sorted database ids, got %+v", config.Databases)
	}
	if config.Databases[1].Name != "tcm-db" {
		t.Fatalf("expected optional database name, got %+v", config.Databases[1])
	}
	if config.Databases[0].MaxOpenConns != 3 || config.Databases[0].MaxIdleConns != defaultMaxIdleConns {
		t.Fatalf("expected herb to use global pool defaults, got %+v", config.Databases[0])
	}
	if config.Databases[1].MaxOpenConns != 8 || config.Databases[1].MaxIdleConns != 2 {
		t.Fatalf("expected tcm to use per-database pool settings, got %+v", config.Databases[1])
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
		"DATABASE_TCM_URL": "sqlite:///tmp/tcm.db",
		envDefaultDatabase: "missing",
	})
	if err == nil {
		t.Fatal("expected unknown default database error")
	}
}

func TestParseConfigRejectsInvalidPoolSettings(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DATABASE_TCM_URL":            "sqlite:///tmp/tcm.db",
		"DATABASE_TCM_MAX_OPEN_CONNS": "1",
		"DATABASE_TCM_MAX_IDLE_CONNS": "2",
	})
	if err == nil {
		t.Fatal("expected invalid pool settings error")
	}
}

func TestParseConfigRejectsDuplicateNormalizedDatabaseID(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DATABASE_TCM_URL": "sqlite:///tmp/tcm.db",
		"DATABASE_tcm_URL": "sqlite:///tmp/tcm2.db",
	})
	if err == nil {
		t.Fatal("expected duplicate database id error")
	}
}
