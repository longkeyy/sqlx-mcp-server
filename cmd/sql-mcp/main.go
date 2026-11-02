// Command sql-mcp exposes SQL databases over the Model Context Protocol.
//
// Modes:
//   - stdio (default, or `sql-mcp stdio`): JSON-RPC on stdin/stdout, logs on stderr.
//   - serve (`sql-mcp serve`): Streamable HTTP and legacy SSE transports.
//
// The deprecated no-subcommand `--listen`/LISTEN startup is kept as an alias of
// serve with a migration notice. Static databases come from --dsn/DB_URL and
// DB_{ID}_URL; --data-dir is accepted for the instance store assembled later.
package main

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
)

// Process exit codes are part of the command contract: 2 means the operator
// asked for something invalid, 1 means a valid configuration failed at runtime.
const (
	exitOK      = 0
	exitRuntime = 1
	exitUsage   = 2
)

func main() {
	// The .env file is a developer convenience; real deployments pass flags or
	// environment variables. Keeping it here (not in init) leaves run testable.
	if _, err := os.Stat(".env"); err == nil {
		if err := godotenv.Load(); err != nil {
			log.Printf("Warning: could not load .env file: %v", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(run(ctx, os.Args[1:], config.EnvMapFromOS(), os.Stdin, os.Stdout, os.Stderr))
}

// run executes one complete lifecycle and returns the process exit code.
// All diagnostic output goes to stderr; stdout is reserved for MCP JSON-RPC in
// stdio mode (usage text is the only exception).
func run(ctx context.Context, args []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) int {
	logger := log.New(stderr, "", log.LstdFlags)

	cfg, err := config.ParseArgs(args, env)
	if errors.Is(err, config.ErrHelp) {
		config.Usage(stdout)
		return exitOK
	}
	if err != nil {
		logger.Printf("Configuration error: %v", err)
		return exitUsage
	}

	for _, notice := range cfg.Notices {
		logger.Printf("Notice: %s", notice)
	}

	if cfg.DataDir != "" {
		dataDir, err := config.PrepareDataDir(cfg.DataDir)
		if err != nil {
			logger.Printf("Configuration error: %v", err)
			return exitUsage
		}
		cfg.DataDir = dataDir
		logger.Printf("Using data directory %s (persistent instance storage is not part of this release)", cfg.DataDir)
	}

	if cfg.Mode == config.ModeStdio {
		return runStdio(ctx, cfg, stdin, stdout, logger)
	}
	return runServe(ctx, cfg, logger)
}

// defaultDatabaseConfig returns the static database selected as default.
func defaultDatabaseConfig(cfg *config.AppConfig) (config.DatabaseConfig, bool) {
	for _, database := range cfg.Databases {
		if database.ID == cfg.DefaultDatabase {
			return database, true
		}
	}
	return config.DatabaseConfig{}, false
}
