package main

import (
	"log"
	"net/http"
	"os"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	mcpserver "github.com/mark3labs/mcp-go/server"
	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
	sqlmcp "github.com/longkeyy/sqlx-mcp-server/internal/server"
)

func init() {
	if _, err := os.Stat(".env"); err == nil {
		if err := godotenv.Load(); err != nil {
			log.Printf("Warning: Error loading .env file: %v\n", err)
		}
	}
}

func main() {
	cfg, err := config.Parse()
	if err != nil {
		log.Fatal(err)
	}

	apps, err := sqlmcp.BuildAppServers(cfg.Databases)
	if err != nil {
		log.Fatal(err)
	}
	defer sqlmcp.CloseAppServers(apps)

	if cfg.Listen == "" {
		log.Printf("Starting %d database instance(s) in stdio mode; default=%s", len(apps), cfg.DefaultDatabase)
		app, err := sqlmcp.GetDefaultApp(apps, cfg.DefaultDatabase)
		if err != nil {
			log.Fatal(err)
		}
		if err := mcpserver.ServeStdio(app.MCPServer()); err != nil {
			log.Printf("Stdio server error: %v\n", err)
		}
		return
	}

	mux := http.NewServeMux()
	if err := sqlmcp.MountHTTPServers(mux, apps, cfg.DefaultDatabase); err != nil {
		log.Fatal(err)
	}
	if err := sqlmcp.MountSSEServers(mux, apps, cfg.DefaultDatabase); err != nil {
		log.Fatal(err)
	}
	log.Printf("Starting %d database instance(s) on %s; default=%s", len(apps), cfg.Listen, cfg.DefaultDatabase)
	if cfg.AuthToken != "" {
		log.Printf("HTTP/SSE authentication enabled")
	}
	log.Printf("MCP endpoints listening on %s/mcp and %s/sse; database endpoints on %s/db/{id}/mcp and %s/db/{id}/sse", cfg.Listen, cfg.Listen, cfg.Listen, cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, sqlmcp.AuthMiddleware(cfg.AuthToken)(mux)); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
