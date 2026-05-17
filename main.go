package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/mark3labs/mcp-go/server"
	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"
)

var (
	db       *sql.DB
	dbDriver DriverType
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

	// Parse DSN
	dsnConfig, err := ParseDSN(config.DSN)
	if err != nil {
		log.Fatalf("Invalid DSN: %v", err)
	}

	// Initialize database connection pool
	if err := initConnectionPool(dsnConfig); err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}
	defer db.Close()

	// Set global driver type
	dbDriver = dsnConfig.Driver

	// Build instance name
	instanceName := "sql-mcp-server"
	if config.Name != "" {
		instanceName = config.Name
	}
	instanceName += " 🚀"

	s := server.NewMCPServer(
		instanceName,
		"1.0.0",
	)

	// Build dynamic tool descriptions based on current database
	dbName := getCurrentDatabase()
	dbVersion := getDBVersion()
	dialectName := getSQLDialectName()

	queryDesc := fmt.Sprintf("Execute a read-oriented SQL query (SELECT, WITH, EXPLAIN, SHOW, DESCRIBE). Results are returned as compact JSON with columns plus row arrays. Current database: %s (%s %s).", dbName, dialectName, dbVersion)
	executeDesc := fmt.Sprintf("Execute a write operation: DML (INSERT/UPDATE/DELETE), DDL (CREATE/ALTER/DROP), or call a stored procedure. Current database: %s (%s %s).", dbName, dialectName, dbVersion)
	listObjectsDesc := fmt.Sprintf("List database objects (tables, views, procedures, functions) as JSON object records. Current database: %s (%s %s).", dbName, dialectName, dbVersion)
	describeDesc := fmt.Sprintf("Describe a database object (table, view, procedure, function) as JSON metadata. Current database: %s (%s %s).", dbName, dialectName, dbVersion)

	// Register tools
	s.AddTool(queryTool(queryDesc), queryToolHandler)
	s.AddTool(executeTool(executeDesc), executeToolHandler)
	s.AddTool(listObjectsTool(listObjectsDesc), listObjectsToolHandler)
	s.AddTool(describeObjectTool(describeDesc), describeObjectToolHandler)

	addr := fmt.Sprintf(":%d", config.Port)

	log.Printf("Starting %s (%s) in %s mode on %s", instanceName, dsnConfig.Driver, config.ServerMode, addr)

	switch config.ServerMode {
	case "stdio":
		if err := server.ServeStdio(s); err != nil {
			log.Printf("Stdio server error: %v\n", err)
		}
	case "sse":
		sse := server.NewSSEServer(s)
		log.Printf("SSE server listening on %s/sse", addr)
		if err := sse.Start(addr); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	case "http":
		httpServer := server.NewStreamableHTTPServer(s)
		log.Printf("StreamableHTTP server listening on %s/mcp", addr)
		if err := httpServer.Start(addr); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	default:
		log.Fatalf("Unknown server mode: %s", config.ServerMode)
	}
}

func initConnectionPool(dsnConfig *DSNConfig) error {
	var err error
	db, err = sql.Open(string(dsnConfig.Driver), dsnConfig.ConnStr)
	if err != nil {
		return err
	}

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)

	if err = db.Ping(); err != nil {
		return err
	}

	log.Printf("Successfully connected to database (%s): %s", dsnConfig.Driver, MaskDSN(dsnConfig.RawDSN))
	return nil
}
