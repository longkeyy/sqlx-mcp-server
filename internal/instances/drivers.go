// Package instances owns the persistent instance registry and its HTTP
// administration API. It never depends on MCP, UI or token packages.
package instances

import (
	// Register every database driver the registry can resolve or test.
	// cmd/sql-mcp registers these too, but this package must not rely on
	// another package's init side effects.
	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"
)
