package main

import (
	"context"
	"errors"
	"github.com/longkeyy/sqlx-mcp-server/internal/instances"
	"github.com/longkeyy/sqlx-mcp-server/internal/routing"
	"io"
	"log"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
	sqlmcp "github.com/longkeyy/sqlx-mcp-server/internal/server"
)

// runStdio serves the default static database over stdin/stdout.
//
// Only the selected database is opened, mirroring an interactive session that
// targets one instance. The MCP library returns nil on EOF and context.Err()
// when the supplied context is cancelled; both are normal shutdowns. Database
// pools are closed by the deferred close so a clean exit releases connections.
func runStdio(ctx context.Context, cfg *config.AppConfig, stdin io.Reader, stdout io.Writer, logger *log.Logger) int {
	if cfg.DataDir != "" {
		manager, err := instances.Open(cfg.DataDir)
		if err != nil {
			logger.Printf("Management store: %v", err)
			return exitRuntime
		}
		defer manager.Close()
		manager.SetExternal(cfg.Databases)
		id := cfg.Instance
		if id == "" && len(cfg.Databases) == 0 {
			id, err = manager.Default(ctx)
			if err != nil {
				return exitRuntime
			}
		}
		if id != "" {
			database, err := manager.Resolve(ctx, id)
			if err != nil {
				logger.Printf("Unknown instance %q", id)
				return exitRuntime
			}
			cfg.Databases = []config.DatabaseConfig{database}
			cfg.DefaultDatabase = id
			router, err := routing.New(manager)
			if err != nil {
				logger.Printf("Stdio router unavailable")
				return exitRuntime
			}
			defer router.Close()
			fixed, err := router.FixedServer(ctx, id)
			if err != nil {
				logger.Printf("Unknown instance %q", id)
				return exitRuntime
			}
			transport := mcpserver.NewStdioServer(fixed)
			transport.SetErrorLogger(logger)
			if err := transport.Listen(ctx, stdin, stdout); err != nil && !errors.Is(err, context.Canceled) {
				logger.Printf("Stdio server error: %v", err)
				return exitRuntime
			}
			return exitOK

		}
	} else if cfg.Instance != "" {
		cfg.DefaultDatabase = cfg.Instance
	}
	database, ok := defaultDatabaseConfig(cfg)
	if !ok {
		logger.Printf("Startup error: no database instance is configured")
		return exitRuntime
	}

	apps, err := sqlmcp.BuildAppServers([]config.DatabaseConfig{database})
	if err != nil {
		logger.Printf("Startup error: %v", err)
		return exitRuntime
	}
	defer sqlmcp.CloseAppServers(apps)

	app, err := sqlmcp.GetDefaultApp(apps, cfg.DefaultDatabase)
	if err != nil {
		logger.Printf("Startup error: %v", err)
		return exitRuntime
	}

	server := mcpserver.NewStdioServer(app.MCPServer())
	server.SetErrorLogger(logger)
	logger.Printf("Serving database %q over stdio; stdout carries JSON-RPC only", database.ID)

	if err := server.Listen(ctx, stdin, stdout); err != nil && !errors.Is(err, context.Canceled) {
		logger.Printf("Stdio server error: %v", err)
		return exitRuntime
	}
	return exitOK
}
