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
	"github.com/mark3labs/mcp-go/server"
	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"
)

func init() {
	if _, err := os.Stat(".env"); err == nil {
		if err := godotenv.Load(); err != nil {
			log.Printf("Warning: Error loading .env file: %v\n", err)
		}
	}
}

func main() {
	// Parse configuration
	config, err := ParseConfig()
	if err != nil {
		log.Fatal(err)
	}

	apps, err := buildAppServers(config.Databases)
	if err != nil {
		log.Fatal(err)
	}
	defer closeAppServers(apps)

	addr := fmt.Sprintf(":%d", config.Port)
	log.Printf("Starting %d database instance(s) in %s mode on %s; default=%s", len(apps), config.ServerMode, addr, config.DefaultDatabase)

	switch config.ServerMode {
	case "stdio":
		app, err := getDefaultApp(apps, config.DefaultDatabase)
		if err != nil {
			log.Fatal(err)
		}
		if err := server.ServeStdio(app.mcpServer); err != nil {
			log.Printf("Stdio server error: %v\n", err)
		}
	case "sse":
		mux := http.NewServeMux()
		if err := mountSSEServers(mux, apps, config.DefaultDatabase); err != nil {
			log.Fatal(err)
		}
		log.Printf("SSE default endpoint listening on %s/sse; database endpoints on %s/db/{id}/sse", addr, addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	case "http":
		mux := http.NewServeMux()
		if err := mountHTTPServers(mux, apps, config.DefaultDatabase); err != nil {
			log.Fatal(err)
		}
		log.Printf("StreamableHTTP default endpoint listening on %s/mcp; database endpoints on %s/db/{id}/mcp", addr, addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	default:
		log.Fatalf("Unknown server mode: %s", config.ServerMode)
	}
}
