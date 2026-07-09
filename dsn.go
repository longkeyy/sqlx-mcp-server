package main

import (
	"fmt"
	"net/url"
	"strings"
)

// DriverType represents the type of database driver
type DriverType string

const (
	DriverMySQL      DriverType = "mysql"
	DriverMariaDB    DriverType = "mariadb"
	DriverPercona    DriverType = "percona"
	DriverTiDB       DriverType = "tidb"
	DriverDoris      DriverType = "doris"
	DriverOceanBase  DriverType = "oceanbase"
	DriverStarRocks  DriverType = "starrocks"
	DriverPostgres   DriverType = "postgres"
	DriverCockroach  DriverType = "cockroach"
	DriverGreenplum  DriverType = "greenplum"
	DriverOpenGauss  DriverType = "opengauss"
	DriverMogDB      DriverType = "mogdb"
	DriverSQLite     DriverType = "sqlite"
	DriverMSSQL      DriverType = "sqlserver"
	DriverClickHouse DriverType = "clickhouse"
)

// DSNConfig holds parsed DSN configuration
type DSNConfig struct {
	Driver    DriverType
	SQLDriver string
	RawDSN    string
	ConnStr   string // Driver-specific connection string
	Database  string
	Host      string
	Port      string
}

// ParseDSN parses a standard URL-style DSN into driver-specific connection string
// Supported formats:
//   - mysql://user:pass@host:port/dbname?params
//   - mariadb://user:pass@host:port/dbname?params
//   - percona://user:pass@host:port/dbname?params
//   - tidb://user:pass@host:port/dbname?params
//   - doris://user:pass@host:port/dbname?params
//   - oceanbase://user:pass@host:port/dbname?params
//   - starrocks://user:pass@host:port/dbname?params
//   - postgres://user:pass@host:port/dbname?params
//   - postgresql://user:pass@host:port/dbname?params
//   - cockroach://user:pass@host:port/dbname?params
//   - greenplum://user:pass@host:port/dbname?params
//   - opengauss://user:pass@host:port/dbname?params
//   - mogdb://user:pass@host:port/dbname?params
//   - sqlite:///path/to/db.sqlite
//   - sqlserver://user:pass@host:port/dbname?params
//   - clickhouse://user:pass@host:port/dbname?params
func ParseDSN(dsn string) (*DSNConfig, error) {
	if dsn == "" {
		return nil, fmt.Errorf("empty DSN")
	}

	// Detect driver from DSN prefix
	driver, dsnWithoutScheme := detectDriver(dsn)
	if driver == "" {
		return nil, fmt.Errorf("unsupported DSN scheme, supported: mysql://, mariadb://, percona://, tidb://, doris://, oceanbase://, starrocks://, postgres://, postgresql://, cockroach://, greenplum://, opengauss://, mogdb://, sqlite://, sqlserver://, clickhouse://")
	}

	// Parse as URL
	u, err := url.Parse(dsnWithoutScheme)
	if err != nil {
		return nil, fmt.Errorf("invalid DSN: %w", err)
	}

	config := &DSNConfig{
		Driver:    driver,
		SQLDriver: string(driver),
		RawDSN:    dsn,
		Database:  strings.TrimPrefix(u.Path, "/"),
		Host:      u.Hostname(),
		Port:      u.Port(),
	}

	// Convert to driver-specific connection string
	switch driver {
	case DriverMySQL:
		config.ConnStr, err = buildMySQLDSN(u, "3306")
	case DriverMariaDB, DriverPercona, DriverOceanBase:
		config.SQLDriver = string(DriverMySQL)
		config.ConnStr, err = buildMySQLDSN(u, "3306")
	case DriverTiDB:
		config.SQLDriver = string(DriverMySQL)
		config.ConnStr, err = buildMySQLDSN(u, "4000")
	case DriverDoris:
		config.SQLDriver = string(DriverMySQL)
		config.ConnStr, err = buildMySQLDSN(u, "9030")
	case DriverStarRocks:
		config.SQLDriver = string(DriverMySQL)
		config.ConnStr, err = buildMySQLDSN(u, "9030")
	case DriverPostgres:
		config.ConnStr = dsnWithoutScheme
	case DriverCockroach, DriverGreenplum, DriverOpenGauss, DriverMogDB:
		config.SQLDriver = string(DriverPostgres)
		config.ConnStr = replaceDSNScheme(dsnWithoutScheme, string(DriverPostgres))
	case DriverSQLite:
		// sqlite:///path/to/db -> /path/to/db
		// sqlite://./relative -> ./relative
		config.ConnStr = u.Path
		if config.ConnStr == "" {
			config.ConnStr = u.Host
		}
		if u.RawQuery != "" {
			config.ConnStr += "?" + u.RawQuery
		}
	case DriverMSSQL:
		config.ConnStr = dsnWithoutScheme
	case DriverClickHouse:
		config.ConnStr = dsnWithoutScheme
	default:
		return nil, fmt.Errorf("unsupported driver: %s", driver)
	}

	if err != nil {
		return nil, err
	}

	return config, nil
}

// detectDriver extracts driver type from DSN scheme
func detectDriver(dsn string) (DriverType, string) {
	schemes := []struct {
		prefix string
		driver DriverType
	}{
		{"mysql://", DriverMySQL},
		{"mariadb://", DriverMariaDB},
		{"percona://", DriverPercona},
		{"tidb://", DriverTiDB},
		{"doris://", DriverDoris},
		{"oceanbase://", DriverOceanBase},
		{"starrocks://", DriverStarRocks},
		{"postgresql://", DriverPostgres},
		{"postgres://", DriverPostgres},
		{"cockroach://", DriverCockroach},
		{"greenplum://", DriverGreenplum},
		{"opengauss://", DriverOpenGauss},
		{"mogdb://", DriverMogDB},
		{"sqlite://", DriverSQLite},
		{"sqlserver://", DriverMSSQL},
		{"clickhouse://", DriverClickHouse},
	}

	for _, s := range schemes {
		if strings.HasPrefix(dsn, s.prefix) {
			return s.driver, dsn
		}
	}

	return "", ""
}

// buildMySQLDSN converts URL format to go-sql-driver format
// mysql://user:pass@host:port/dbname?params -> user:pass@tcp(host:port)/dbname?params
func buildMySQLDSN(u *url.URL, defaultPort string) (string, error) {
	user := u.User.Username()
	pass, _ := u.User.Password()

	addr := u.Host
	if u.Port() == "" {
		addr = u.Host + ":" + defaultPort
	}

	connStr := fmt.Sprintf("%s:%s@tcp(%s)/%s", user, pass, addr, strings.TrimPrefix(u.Path, "/"))

	// Append query parameters
	if u.RawQuery != "" {
		connStr += "?" + u.RawQuery
	}

	return connStr, nil
}

func replaceDSNScheme(dsn, scheme string) string {
	if idx := strings.Index(dsn, "://"); idx != -1 {
		return scheme + dsn[idx:]
	}
	return dsn
}
