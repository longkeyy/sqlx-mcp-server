package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// ModeStdio serves MCP over standard input/output.
	ModeStdio = "stdio"
	// ModeServe serves MCP over HTTP and SSE.
	ModeServe = "serve"

	// DefaultServeListen keeps `sql-mcp serve` on loopback unless the caller
	// explicitly opts into a wider bind address.
	DefaultServeListen = "127.0.0.1:8088"

	envDataDir = "DATA_DIR"

	dataDirPerm = 0o700
)

// Notices are operator-facing messages that ParseArgs attaches to an
// AppConfig. The command entrypoint logs them to stderr at startup.
const (
	NoticeLegacyHTTP  = "starting HTTP without a subcommand is deprecated; use \"sql-mcp serve --listen ADDR\" instead"
	NoticeStdioListen = "ignoring --listen in stdio mode; stdio never opens a network listener"
)

// ErrHelp signals that usage should be printed and the process should exit
// successfully. It is returned for `help`, `-h` and `--help`.
var ErrHelp = errors.New("help requested")

// ParseArgs resolves the command, flags and environment into an AppConfig.
//
// Supported forms:
//
//	sql-mcp [flags]                  stdio, or legacy HTTP when --listen/LISTEN is set
//	sql-mcp stdio [flags]            stdio; ignores --listen and LISTEN
//	sql-mcp serve [flags]            HTTP/SSE; defaults to 127.0.0.1:8088
//	sql-mcp help | -h | --help       usage
//
// Flags may appear before or after the command. Parsing performs no I/O: the
// data directory is only normalised here and created by PrepareDataDir at
// startup, so configuration can be inspected by callers and tests safely.
func ParseArgs(args []string, env map[string]string) (*AppConfig, error) {
	var values flagValues
	var instance string
	flags := flag.NewFlagSet("sql-mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&instance, "instance", "", "Explicit database instance for stdio")
	flags.StringVar(&values.dsn, "dsn", "", "Database DSN (e.g., mysql://user:pass@host:3306/db)")
	flags.StringVar(&values.name, "name", "", "Instance name for MCP server display/logging (optional)")
	flags.StringVar(&values.listen, "listen", "", "HTTP listen address (e.g., :8088 or 127.0.0.1:8088)")
	flags.StringVar(&values.dataDir, "data-dir", "", "Directory for persistent management data")
	flags.StringVar(&values.authToken, "auth-token", "", "HTTP/SSE bearer token. Empty disables built-in authentication.")

	mode, explicitStdio, err := parseCommand(flags, args)
	if err != nil {
		return nil, err
	}

	explicitListen := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "listen" {
			explicitListen = true
		}
	})

	listenEnv := strings.TrimSpace(env[envListen])
	appConfig := &AppConfig{
		Mode:          mode,
		Instance:      strings.TrimSpace(instance),
		ExplicitStdio: explicitStdio,
		DSN:           firstNonEmpty(values.dsn, env[envDatabaseURL]),
		Name:          firstNonEmpty(values.name, env[envDatabaseName]),
		AuthToken:     strings.TrimSpace(firstNonEmpty(values.authToken, env[envAuthToken])),
		DataDir:       cleanPath(firstNonEmpty(values.dataDir, env[envDataDir])),
	}

	if mode == "" {
		// Backwards compatibility: a listen address in the environment or on
		// the command line keeps the original no-subcommand HTTP startup.
		if (explicitListen && strings.TrimSpace(values.listen) != "") || listenEnv != "" {
			appConfig.Mode = ModeServe
			appConfig.LegacyHTTP = true
			appConfig.Notices = append(appConfig.Notices, NoticeLegacyHTTP)
		} else {
			appConfig.Mode = ModeStdio
		}
	}

	switch appConfig.Mode {
	case ModeStdio:
		// Explicit stdio must never open a listener, even if LISTEN or
		// --listen was provided for a launcher's convenience.
		appConfig.Listen = ""
		if explicitStdio && explicitListen {
			appConfig.Notices = append(appConfig.Notices, NoticeStdioListen)
		}
	case ModeServe:
		switch {
		case explicitListen && strings.TrimSpace(values.listen) != "":
			appConfig.Listen = strings.TrimSpace(values.listen)
		case listenEnv != "":
			appConfig.Listen = listenEnv
		default:
			appConfig.Listen = DefaultServeListen
		}
	}

	databases, defaultDatabase, err := parseDatabaseConfigs(
		appConfig.DSN,
		appConfig.Name,
		strings.TrimSpace(env[envDatabaseMemo]),
		env,
		servesWithoutDatabases(appConfig),
	)
	if err != nil {
		return nil, err
	}
	appConfig.Databases = databases
	appConfig.DefaultDatabase = defaultDatabase
	if appConfig.DSN == "" && defaultDatabase != "" {
		appConfig.DSN = databaseByID(databases, defaultDatabase).DSN
	}
	if appConfig.Name == "" && defaultDatabase != "" {
		appConfig.Name = databaseByID(databases, defaultDatabase).Name
	}

	return appConfig, nil
}

// parseCommand consumes an optional leading command token. The flag package
// stops at the first positional argument, so flags both before and after the
// command are parsed by re-running Parse on the remainder with the same
// FlagSet. An empty mode means the legacy no-subcommand form.
func parseCommand(flags *flag.FlagSet, args []string) (mode string, explicitStdio bool, err error) {
	remaining := args
	for {
		if err := flags.Parse(remaining); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return "", false, ErrHelp
			}
			return "", false, err
		}
		if flags.NArg() == 0 {
			return mode, explicitStdio, nil
		}
		if mode != "" {
			return "", false, fmt.Errorf("unexpected argument %q; run \"sql-mcp help\" for usage", flags.Arg(0))
		}
		switch flags.Arg(0) {
		case "stdio":
			mode = ModeStdio
			explicitStdio = true
		case "serve":
			mode = ModeServe
		case "help":
			return "", false, ErrHelp
		default:
			return "", false, fmt.Errorf("unknown command %q; run \"sql-mcp help\" for usage", flags.Arg(0))
		}
		remaining = flags.Args()[1:]
		if len(remaining) == 0 {
			return mode, explicitStdio, nil
		}
	}
}

// servesWithoutDatabases reports whether startup may continue with no
// statically configured database. Only the explicit serve command is expected
// to stay healthy without any instance; stdio and the legacy HTTP startup keep
// requiring a DSN.
func servesWithoutDatabases(cfg *AppConfig) bool {
	return (cfg.Mode == ModeServe && !cfg.LegacyHTTP) || cfg.DataDir != ""
}

// PrepareDataDir creates the configured data directory with owner-only
// permissions and returns its absolute path. An empty path is a no-op. Any
// existing path must be a directory. The application calls this once at
// startup; task-owned instance storage will live there once assembled.
func PrepareDataDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve data dir %q: %w", path, err)
	}
	if err := os.MkdirAll(absolute, dataDirPerm); err != nil {
		return "", fmt.Errorf("prepare data dir %q: %w", absolute, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect data dir %q: %w", absolute, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("data dir %q is not a directory", absolute)
	}
	return absolute, nil
}

func cleanPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

type flagValues struct {
	dsn       string
	name      string
	listen    string
	dataDir   string
	authToken string
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// parseConfig is the legacy entry point; it applies the same rules as
// ParseArgs without a subcommand.
func parseConfig(args []string, env map[string]string) (*AppConfig, error) {
	return ParseArgs(args, env)
}

// Usage writes the command help text.
func Usage(w io.Writer) {
	fmt.Fprintf(w, `sql-mcp - MCP server for SQL databases

Usage:
  sql-mcp [flags]           Run MCP over stdin/stdout (default)
  sql-mcp stdio [flags]     Run MCP over stdin/stdout; ignores LISTEN
  sql-mcp serve [flags]     Run the HTTP/SSE server
  sql-mcp help              Show this help

Flags:
  --dsn string          Database DSN (default: DB_URL, or DB_{ID}_URL)
  --name string         Instance display name (default: DB_NAME)
  --listen string       HTTP listen address for serve (default: LISTEN, then %s)
  --data-dir string     Directory for persistent management data (default: DATA_DIR)
  --auth-token string   HTTP/SSE bearer token (default: AUTH_TOKEN)

Environment:
  DB_URL, DB_NAME, DB_MEMO, DEFAULT_DB
  DB_{ID}_URL, DB_{ID}_NAME, DB_{ID}_MEMO
  DB_MAX_OPEN_CONNS, DB_MAX_IDLE_CONNS
  LISTEN, DATA_DIR, AUTH_TOKEN

Exit status:
  0  normal shutdown (also help output)
  1  runtime failure while serving
  2  invalid command line or environment configuration

Routes:
  GET  /healthz         Liveness probe; always 200 while the process is up
  POST /mcp             Streamable HTTP MCP endpoint (default instance)
  GET  /sse             Legacy SSE MCP endpoint (default instance)
  POST /message         Legacy SSE message endpoint (default instance)
  POST /db/{id}/mcp     Streamable HTTP MCP endpoint for one instance
  GET  /db/{id}/sse     Legacy SSE MCP endpoint for one instance
  POST /db/{id}/message Legacy SSE message endpoint for one instance

Notes:
  A listen address without a subcommand keeps the legacy HTTP startup and
  logs a migration notice. Bare "sql-mcp" starts stdio and requires a database DSN.
  "sql-mcp serve" may start with no configured database; /healthz still reports
  liveness while MCP routes answer 503 until an instance becomes available.
`, DefaultServeListen)
}
