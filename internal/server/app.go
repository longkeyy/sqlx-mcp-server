package server

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
	"github.com/longkeyy/sqlx-mcp-server/internal/dsn"
)

type AppServer struct {
	id        string
	name      string
	db        *sql.DB
	driver    dsn.Driver
	dsnConfig *dsn.Config
	mcpServer *mcpserver.MCPServer
}

func newAppServer(cfg config.DatabaseConfig) (*AppServer, error) {
	dsnConfig, err := dsn.Parse(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("invalid DSN for database %q: %w", cfg.ID, err)
	}

	db, err := openConnectionPool(dsnConfig, cfg)
	if err != nil {
		return nil, fmt.Errorf("database %q connection failed: %w", cfg.ID, err)
	}

	app := &AppServer{
		id:        cfg.ID,
		name:      cfg.Name,
		db:        db,
		driver:    dsnConfig.Driver,
		dsnConfig: dsnConfig,
	}
	app.mcpServer = app.buildMCPServer()
	log.Printf("Successfully connected to database %q (%s): %s", app.id, app.driver, config.MaskDSN(dsnConfig.RawDSN))
	return app, nil
}

func openConnectionPool(dsnConfig *dsn.Config, cfg config.DatabaseConfig) (*sql.DB, error) {
	db, err := sql.Open(dsnConfig.SQLDriver, dsnConfig.ConnStr)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)

	if err = db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func (a *AppServer) close() {
	if a.db != nil {
		_ = a.db.Close()
	}
}

func (a *AppServer) MCPServer() *mcpserver.MCPServer {
	return a.mcpServer
}

func (a *AppServer) buildMCPServer() *mcpserver.MCPServer {
	instanceName := a.name
	if instanceName == "" {
		instanceName = "sql-mcp-" + a.id
	}
	instanceName += " 🚀"

	s := mcpserver.NewMCPServer(instanceName, "1.0.0")

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

func BuildAppServers(configs []config.DatabaseConfig) (map[string]*AppServer, error) {
	apps := make(map[string]*AppServer, len(configs))
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

func CloseAppServers(apps map[string]*AppServer) {
	for _, app := range apps {
		app.close()
	}
}

func GetDefaultApp(apps map[string]*AppServer, defaultID string) (*AppServer, error) {
	app := apps[defaultID]
	if app == nil {
		return nil, fmt.Errorf("default database %q was not initialized", defaultID)
	}
	return app, nil
}

func MountHTTPServers(mux *http.ServeMux, apps map[string]*AppServer, defaultID string) error {
	defaultApp, err := GetDefaultApp(apps, defaultID)
	if err != nil {
		return err
	}
	mux.Handle("/mcp", mcpserver.NewStreamableHTTPServer(defaultApp.mcpServer))

	for id, app := range apps {
		mux.Handle("/db/"+id+"/mcp", mcpserver.NewStreamableHTTPServer(app.mcpServer, mcpserver.WithEndpointPath("/db/"+id+"/mcp")))
	}
	return nil
}

func MountSSEServers(mux *http.ServeMux, apps map[string]*AppServer, defaultID string) error {
	defaultApp, err := GetDefaultApp(apps, defaultID)
	if err != nil {
		return err
	}
	defaultSSE := mcpserver.NewSSEServer(defaultApp.mcpServer, mcpserver.WithAppendQueryToMessageEndpoint())
	mux.Handle("/sse", defaultSSE)
	mux.Handle("/message", defaultSSE)

	for id, app := range apps {
		sse := mcpserver.NewSSEServer(
			app.mcpServer,
			mcpserver.WithSSEEndpoint("/db/"+id+"/sse"),
			mcpserver.WithMessageEndpoint("/db/"+id+"/message"),
			mcpserver.WithAppendQueryToMessageEndpoint(),
		)
		mux.Handle("/db/"+id+"/sse", sse)
		mux.Handle("/db/"+id+"/message", sse)
	}
	return nil
}
