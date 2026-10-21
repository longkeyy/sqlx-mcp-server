package main

import (
	"context"
	"errors"
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
