package config

import "testing"

// Pool limits inherit per-id > global > built-in, but validation used to run
// per scope, so DB_APP_MAX_IDLE_CONNS=30 was rejected against the built-in
// open limit of 25 even when DB_MAX_OPEN_CONNS=50 allowed it. These cases pin
// validation of the resolved pair, and the message naming which key or the
// built-in default supplied each side.

func TestParseConfigValidatesInheritedPoolLimits(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":            "sqlite:///tmp/app.db",
		"DB_MAX_OPEN_CONNS":     "50",
		"DB_APP_MAX_IDLE_CONNS": "30",
	})
	if err != nil {
		t.Fatal(err)
	}
	database := config.Databases[0]
	if database.MaxOpenConns != 50 || database.MaxIdleConns != 30 {
		t.Fatalf("expected the inherited open limit to allow 30 idle connections, got %+v", database)
	}
}

func TestParseConfigRejectsIdleAboveInheritedOpen(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":            "sqlite:///tmp/app.db",
		"DB_MAX_OPEN_CONNS":     "10",
		"DB_APP_MAX_IDLE_CONNS": "40",
	})
	assertErrorMentions(t, err,
		`database "app"`,
		"max idle connections 40 (DB_APP_MAX_IDLE_CONNS)",
		"max open connections 10 (DB_MAX_OPEN_CONNS)")
}

func TestParseConfigNamesBuiltInOpenLimit(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":            "sqlite:///tmp/app.db",
		"DB_APP_MAX_IDLE_CONNS": "40",
	})
	assertErrorMentions(t, err,
		`database "app"`,
		"max idle connections 40 (DB_APP_MAX_IDLE_CONNS)",
		"max open connections 25 (default)")
}

func TestParseConfigRejectsDefaultPoolIdleAboveOpen(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_URL":                    "sqlite:///tmp/app.db",
		"DB_MAX_OPEN_CONNS":         "10",
		"DB_DEFAULT_MAX_IDLE_CONNS": "40",
	})
	assertErrorMentions(t, err,
		`database "default"`,
		"max idle connections 40 (DB_DEFAULT_MAX_IDLE_CONNS)",
		"max open connections 10 (DB_MAX_OPEN_CONNS)")
}

func TestParseConfigAcceptsEqualPoolLimits(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":            "sqlite:///tmp/app.db",
		"DB_APP_MAX_OPEN_CONNS": "5",
		"DB_APP_MAX_IDLE_CONNS": "5",
		"DB_MAX_IDLE_CONNS":     "9",
	})
	if err != nil {
		t.Fatal(err)
	}
	database := config.Databases[0]
	if database.MaxOpenConns != 5 || database.MaxIdleConns != 5 {
		t.Fatalf("expected equal per-id limits to be accepted, got %+v", database)
	}
}
