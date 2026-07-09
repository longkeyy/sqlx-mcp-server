package main

import (
	"fmt"
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

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("Starting %d database instance(s) in %s mode on %s; default=%s", len(apps), cfg.ServerMode, addr, cfg.DefaultDatabase)

	switch cfg.ServerMode {
	case "stdio":
		app, err := sqlmcp.GetDefaultApp(apps, cfg.DefaultDatabase)
		if err != nil {
			log.Fatal(err)
		}
		if err := mcpserver.ServeStdio(app.MCPServer()); err != nil {
			log.Printf("Stdio server error: %v\n", err)
		}
	case "sse":
		mux := http.NewServeMux()
		if err := sqlmcp.MountSSEServers(mux, apps, cfg.DefaultDatabase); err != nil {
			log.Fatal(err)
		}
		log.Printf("SSE default endpoint listening on %s/sse; database endpoints on %s/db/{id}/sse", addr, addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	case "http":
		mux := http.NewServeMux()
		if err := sqlmcp.MountHTTPServers(mux, apps, cfg.DefaultDatabase); err != nil {
			log.Fatal(err)
		}
		log.Printf("StreamableHTTP default endpoint listening on %s/mcp; database endpoints on %s/db/{id}/mcp", addr, addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	default:
		log.Fatalf("Unknown server mode: %s", cfg.ServerMode)
	}
}
