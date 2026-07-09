package main

import "testing"

func TestParseDSNMySQLCompatibleDrivers(t *testing.T) {
	tests := []struct {
		name       string
		dsn        string
		driver     DriverType
		sqlDriver  string
		connSubstr string
	}{
		{
			name:       "mariadb default port",
			dsn:        "mariadb://user:pass@mariadb.example/app",
			driver:     DriverMariaDB,
			sqlDriver:  string(DriverMySQL),
			connSubstr: "user:pass@tcp(mariadb.example:3306)/app",
		},
		{
			name:       "oceanbase default mysql port",
			dsn:        "oceanbase://user:pass@oceanbase.example/app",
			driver:     DriverOceanBase,
			sqlDriver:  string(DriverMySQL),
			connSubstr: "user:pass@tcp(oceanbase.example:3306)/app",
		},
		{
			name:       "starrocks default query port",
			dsn:        "starrocks://user:pass@starrocks.example/warehouse",
			driver:     DriverStarRocks,
			sqlDriver:  string(DriverMySQL),
			connSubstr: "user:pass@tcp(starrocks.example:9030)/warehouse",
		},
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

func TestParseDSNPostgresCompatibleDrivers(t *testing.T) {
	tests := []struct {
		name      string
		dsn       string
		driver    DriverType
		connStr   string
		sqlDriver string
	}{
		{
			name:      "cockroach",
			dsn:       "cockroach://user:pass@roach.example:26257/app?sslmode=disable",
			driver:    DriverCockroach,
			sqlDriver: string(DriverPostgres),
			connStr:   "postgres://user:pass@roach.example:26257/app?sslmode=disable",
		},
		{
			name:      "greenplum",
			dsn:       "greenplum://user:pass@gp.example:5432/warehouse",
			driver:    DriverGreenplum,
			sqlDriver: string(DriverPostgres),
			connStr:   "postgres://user:pass@gp.example:5432/warehouse",
		},
		{
			name:      "opengauss",
			dsn:       "opengauss://user:pass@og.example:5432/app",
			driver:    DriverOpenGauss,
			sqlDriver: string(DriverPostgres),
			connStr:   "postgres://user:pass@og.example:5432/app",
		},
		{
			name:      "mogdb",
			dsn:       "mogdb://user:pass@mogdb.example:5432/app",
			driver:    DriverMogDB,
			sqlDriver: string(DriverPostgres),
			connStr:   "postgres://user:pass@mogdb.example:5432/app",
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
			if config.ConnStr != tt.connStr {
				t.Fatalf("expected conn str %q, got %q", tt.connStr, config.ConnStr)
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
