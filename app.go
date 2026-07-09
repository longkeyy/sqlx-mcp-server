package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

type appServer struct {
	id        string
	name      string
	db        *sql.DB
	driver    DriverType
	dsnConfig *DSNConfig
	mcpServer *server.MCPServer
}

func newAppServer(config DatabaseConfig) (*appServer, error) {
	dsnConfig, err := ParseDSN(config.DSN)
	if err != nil {
		return nil, fmt.Errorf("invalid DSN for database %q: %w", config.ID, err)
	}

	db, err := openConnectionPool(dsnConfig, config)
	if err != nil {
		return nil, fmt.Errorf("database %q connection failed: %w", config.ID, err)
	}

	app := &appServer{
		id:        config.ID,
		name:      config.Name,
		db:        db,
		driver:    dsnConfig.Driver,
		dsnConfig: dsnConfig,
	}
	app.mcpServer = app.buildMCPServer()
	log.Printf("Successfully connected to database %q (%s): %s", app.id, app.driver, MaskDSN(dsnConfig.RawDSN))
	return app, nil
}

func openConnectionPool(dsnConfig *DSNConfig, config DatabaseConfig) (*sql.DB, error) {
	db, err := sql.Open(dsnConfig.SQLDriver, dsnConfig.ConnStr)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(config.MaxOpenConns)
	db.SetMaxIdleConns(config.MaxIdleConns)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)

	if err = db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func (a *appServer) close() {
	if a.db != nil {
		_ = a.db.Close()
	}
}

func (a *appServer) buildMCPServer() *server.MCPServer {
	instanceName := a.name
	if instanceName == "" {
		instanceName = "sql-mcp-" + a.id
	}
	instanceName += " 🚀"

	s := server.NewMCPServer(instanceName, "1.0.0")

	dbName := a.getCurrentDatabase()
	dbVersion := a.getDBVersion()
	dialectName := a.getSQLDialectName()

	queryDesc := fmt.Sprintf("Execute a read-oriented SQL query (SELECT, WITH, EXPLAIN, SHOW, DESCRIBE). Results are returned as compact JSON with columns plus row arrays. Current database: %s (%s %s).", dbName, dialectName, dbVersion)
	executeDesc := fmt.Sprintf("Execute a write operation: DML (INSERT/UPDATE/DELETE), DDL (CREATE/ALTER/DROP), or call a stored procedure. Current database: %s (%s %s).", dbName, dialectName, dbVersion)
	listObjectsDesc := fmt.Sprintf("List database objects (tables, views, procedures, functions) as JSON object records. Current database: %s (%s %s).", dbName, dialectName, dbVersion)
	describeDesc := fmt.Sprintf("Describe a database object (table, view, procedure, function) as JSON metadata. Current database: %s (%s %s).", dbName, dialectName, dbVersion)

	s.AddTool(queryTool(queryDesc), a.queryToolHandler)
	s.AddTool(executeTool(executeDesc), a.executeToolHandler)
	s.AddTool(listObjectsTool(listObjectsDesc), a.listObjectsToolHandler)
	s.AddTool(describeObjectTool(describeDesc), a.describeObjectToolHandler)

	return s
}

func buildAppServers(configs []DatabaseConfig) (map[string]*appServer, error) {
	apps := make(map[string]*appServer, len(configs))
	for _, config := range configs {
		app, err := newAppServer(config)
		if err != nil {
			for _, existing := range apps {
				existing.close()
			}
			return nil, err
		}
		apps[app.id] = app
	}
	return apps, nil
}

func closeAppServers(apps map[string]*appServer) {
	for _, app := range apps {
		app.close()
	}
}

func getDefaultApp(apps map[string]*appServer, defaultID string) (*appServer, error) {
	app := apps[defaultID]
	if app == nil {
		return nil, fmt.Errorf("default database %q was not initialized", defaultID)
	}
	return app, nil
}

func mountHTTPServers(mux *http.ServeMux, apps map[string]*appServer, defaultID string) error {
	defaultApp, err := getDefaultApp(apps, defaultID)
	if err != nil {
		return err
	}
	mux.Handle("/mcp", server.NewStreamableHTTPServer(defaultApp.mcpServer))

	for id, app := range apps {
		mux.Handle("/db/"+id+"/mcp", server.NewStreamableHTTPServer(app.mcpServer, server.WithEndpointPath("/db/"+id+"/mcp")))
	}
	return nil
}

func mountSSEServers(mux *http.ServeMux, apps map[string]*appServer, defaultID string) error {
	defaultApp, err := getDefaultApp(apps, defaultID)
	if err != nil {
		return err
	}
	defaultSSE := server.NewSSEServer(defaultApp.mcpServer)
	mux.Handle("/sse", defaultSSE)
	mux.Handle("/message", defaultSSE)

	for id, app := range apps {
		sse := server.NewSSEServer(
			app.mcpServer,
			server.WithSSEEndpoint("/db/"+id+"/sse"),
			server.WithMessageEndpoint("/db/"+id+"/message"),
		)
		mux.Handle("/db/"+id+"/sse", sse)
		mux.Handle("/db/"+id+"/message", sse)
	}
	return nil
}
