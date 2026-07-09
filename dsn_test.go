package main

import "testing"

func TestParseDSNAliasedMySQLDrivers(t *testing.T) {
	tests := []struct {
		name       string
		dsn        string
		driver     DriverType
		sqlDriver  string
		connSubstr string
	}{
		{
			name:       "tidb default port",
			dsn:        "tidb://user:pass@tidb.example/test",
			driver:     DriverTiDB,
			sqlDriver:  string(DriverMySQL),
			connSubstr: "user:pass@tcp(tidb.example:4000)/test",
		},
		{
			name:       "doris default query port",
			dsn:        "doris://user:pass@doris.example/warehouse?parseTime=true",
			driver:     DriverDoris,
			sqlDriver:  string(DriverMySQL),
			connSubstr: "user:pass@tcp(doris.example:9030)/warehouse?parseTime=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := ParseDSN(tt.dsn)
			if err != nil {
				t.Fatal(err)
			}
			if config.Driver != tt.driver {
				t.Fatalf("expected driver %q, got %q", tt.driver, config.Driver)
			}
			if config.SQLDriver != tt.sqlDriver {
				t.Fatalf("expected sql driver %q, got %q", tt.sqlDriver, config.SQLDriver)
			}
			if config.ConnStr != tt.connSubstr {
				t.Fatalf("expected conn str %q, got %q", tt.connSubstr, config.ConnStr)
			}
		})
	}
}

func TestParseDSNClickHouse(t *testing.T) {
	config, err := ParseDSN("clickhouse://user:pass@clickhouse.example:9000/default?secure=true")
	if err != nil {
		t.Fatal(err)
	}
	if config.Driver != DriverClickHouse {
		t.Fatalf("expected clickhouse driver, got %q", config.Driver)
	}
	if config.SQLDriver != string(DriverClickHouse) {
		t.Fatalf("expected clickhouse sql driver, got %q", config.SQLDriver)
	}
	if config.ConnStr != "clickhouse://user:pass@clickhouse.example:9000/default?secure=true" {
		t.Fatalf("unexpected conn str %q", config.ConnStr)
	}
}
