package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testSQLiteDSN = "sqlite:///tmp/example.db"

func TestParseArgsDefaultsToStdioWithoutListener(t *testing.T) {
	cfg, err := ParseArgs([]string{"--dsn", testSQLiteDSN}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeStdio {
		t.Fatalf("expected stdio mode, got %q", cfg.Mode)
	}
	if cfg.ExplicitStdio {
		t.Fatal("bare invocation must not be reported as explicit stdio")
	}
	if cfg.Listen != "" {
		t.Fatalf("stdio must not keep a listen address, got %q", cfg.Listen)
	}
	if cfg.LegacyHTTP {
		t.Fatal("bare invocation must not be legacy HTTP")
	}
	if len(cfg.Notices) != 0 {
		t.Fatalf("unexpected notices: %v", cfg.Notices)
	}
	if cfg.DefaultDatabase != defaultDatabaseID || len(cfg.Databases) != 1 {
		t.Fatalf("unexpected databases: %+v", cfg.Databases)
	}
}

func TestParseArgsExplicitStdioIgnoresListener(t *testing.T) {
	cfg, err := ParseArgs(
		[]string{"stdio", "--dsn", testSQLiteDSN, "--listen", "127.0.0.1:19999"},
		map[string]string{envListen: "127.0.0.1:18888"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeStdio || !cfg.ExplicitStdio {
		t.Fatalf("expected explicit stdio, got mode=%q explicit=%t", cfg.Mode, cfg.ExplicitStdio)
	}
	if cfg.Listen != "" {
		t.Fatalf("stdio must ignore --listen and LISTEN, got %q", cfg.Listen)
	}
	if !slices.Contains(cfg.Notices, NoticeStdioListen) {
		t.Fatalf("expected stdio --listen notice, got %v", cfg.Notices)
	}
}

func TestParseArgsExplicitStdioWithoutListenHasNoNotice(t *testing.T) {
	cfg, err := ParseArgs(
		[]string{"stdio", "--dsn", testSQLiteDSN},
		map[string]string{envListen: "127.0.0.1:18888"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "" {
		t.Fatalf("stdio must ignore LISTEN, got %q", cfg.Listen)
	}
	if len(cfg.Notices) != 0 {
		t.Fatalf("no notice expected without --listen, got %v", cfg.Notices)
	}
}

func TestParseArgsServeDefaultsToLoopback(t *testing.T) {
	cfg, err := ParseArgs([]string{"serve"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeServe {
		t.Fatalf("expected serve mode, got %q", cfg.Mode)
	}
	if cfg.Listen != DefaultServeListen {
		t.Fatalf("expected loopback default %q, got %q", DefaultServeListen, cfg.Listen)
	}
	if DefaultServeListen != "127.0.0.1:8088" {
		t.Fatalf("default serve listen must stay loopback, got %q", DefaultServeListen)
	}
	if cfg.LegacyHTTP {
		t.Fatal("serve subcommand must not be marked legacy")
	}
	if len(cfg.Databases) != 0 || cfg.DefaultDatabase != "" {
		t.Fatalf("serve without DSN should start with no instances, got %+v", cfg.Databases)
	}
}

func TestParseArgsServeListenPrecedence(t *testing.T) {
	cfg, err := ParseArgs(
		[]string{"serve", "--listen", "127.0.0.1:18088"},
		map[string]string{envListen: ":19090", envDataDir: "/env/data"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:18088" {
		t.Fatalf("expected CLI listen to win, got %q", cfg.Listen)
	}

	cfg, err = ParseArgs([]string{"serve"}, map[string]string{envListen: ":19090"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":19090" {
		t.Fatalf("expected LISTEN fallback, got %q", cfg.Listen)
	}
}

func TestParseArgsLegacyHTTPNotice(t *testing.T) {
	cfg, err := ParseArgs([]string{"--dsn", testSQLiteDSN, "--listen", ":18088"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeServe || !cfg.LegacyHTTP {
		t.Fatalf("expected legacy serve, got mode=%q legacy=%t", cfg.Mode, cfg.LegacyHTTP)
	}
	if cfg.Listen != ":18088" {
		t.Fatalf("expected legacy listen address, got %q", cfg.Listen)
	}
	if !slices.Contains(cfg.Notices, NoticeLegacyHTTP) {
		t.Fatalf("expected legacy HTTP notice, got %v", cfg.Notices)
	}

	cfg, err = ParseArgs(nil, map[string]string{
		envDatabaseURL: testSQLiteDSN,
		envListen:      ":18089",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeServe || !cfg.LegacyHTTP || cfg.Listen != ":18089" {
		t.Fatalf("expected LISTEN to keep legacy HTTP, got mode=%q legacy=%t listen=%q", cfg.Mode, cfg.LegacyHTTP, cfg.Listen)
	}
	if !slices.Contains(cfg.Notices, NoticeLegacyHTTP) {
		t.Fatalf("expected legacy HTTP notice, got %v", cfg.Notices)
	}
}

func TestParseArgsLegacyHTTPStillRequiresDSN(t *testing.T) {
	_, err := ParseArgs([]string{"--listen", ":18088"}, nil)
	if err == nil {
		t.Fatal("legacy HTTP startup must keep requiring a DSN")
	}
}

func TestParseArgsStdioRequiresDSN(t *testing.T) {
	for _, args := range [][]string{nil, {"stdio"}} {
		if _, err := ParseArgs(args, nil); err == nil {
			t.Fatalf("expected missing DSN error for args %v", args)
		}
	}
}

func TestParseArgsServeRejectsUnknownDefaultDatabase(t *testing.T) {
	_, err := ParseArgs([]string{"serve"}, map[string]string{envDefaultDatabase: "missing"})
	if err == nil {
		t.Fatal("serve must not accept DEFAULT_DB without a configured database")
	}
}

func TestParseArgsDataDirIsNormalizedButNotCreated(t *testing.T) {
	parent := t.TempDir()
	raw := filepath.Join(parent, "nested", "..", "nested") + string(filepath.Separator)
	cfg, err := ParseArgs([]string{"serve", "--data-dir", raw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != filepath.Clean(raw) {
		t.Fatalf("expected cleaned data dir %q, got %q", filepath.Clean(raw), cfg.DataDir)
	}
	if _, err := os.Stat(cfg.DataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parse-time must not create the data dir, stat err=%v", err)
	}

	cfg, err = ParseArgs([]string{"serve"}, map[string]string{envDataDir: "  " + parent + "  "})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != parent {
		t.Fatalf("expected DATA_DIR fallback, got %q", cfg.DataDir)
	}

	cfg, err = ParseArgs([]string{"serve", "--data-dir", filepath.Join(parent, "cli")}, map[string]string{envDataDir: filepath.Join(parent, "env")})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != filepath.Join(parent, "cli") {
		t.Fatalf("expected CLI data dir to win, got %q", cfg.DataDir)
	}
}

func TestParseArgsAcceptsFlagsBeforeCommand(t *testing.T) {
	cfg, err := ParseArgs([]string{"--dsn", testSQLiteDSN, "--listen", ":18088", "serve", "--data-dir", "/tmp/data"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeServe || cfg.Listen != ":18088" || cfg.DataDir != "/tmp/data" {
		t.Fatalf("unexpected config: mode=%q listen=%q dataDir=%q", cfg.Mode, cfg.Listen, cfg.DataDir)
	}
	if cfg.DSN != testSQLiteDSN {
		t.Fatalf("expected DSN to survive command parsing, got %q", cfg.DSN)
	}
}

func TestParseArgsHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}, {"serve", "-h"}, {"--dsn", testSQLiteDSN, "help"}} {
		if _, err := ParseArgs(args, nil); !errors.Is(err, ErrHelp) {
			t.Fatalf("expected ErrHelp for %v, got %v", args, err)
		}
	}
}

func TestParseArgsRejectsBadCommands(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {"serve", "extra"}, {"stdio", "serve"}} {
		if _, err := ParseArgs(args, nil); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}

func TestParseArgsSelectsNamedDefault(t *testing.T) {
	cfg, err := ParseArgs([]string{"serve"}, map[string]string{
		"DB_REPORTING_URL": "sqlite:///tmp/reporting.db",
		envDefaultDatabase: "REPORTING",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultDatabase != "reporting" || cfg.DSN != "sqlite:///tmp/reporting.db" {
		t.Fatalf("expected reporting as default, got default=%q dsn=%q", cfg.DefaultDatabase, cfg.DSN)
	}
	if len(cfg.Databases) != 1 {
		t.Fatalf("expected one database, got %+v", cfg.Databases)
	}
}

func TestPrepareDataDir(t *testing.T) {
	if path, err := PrepareDataDir("  "); err != nil || path != "" {
		t.Fatalf("empty data dir should be a no-op, got %q, %v", path, err)
	}

	base := t.TempDir()
	target := filepath.Join(base, "store", "nested")
	path, err := PrepareDataDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) || path != target {
		t.Fatalf("expected absolute %q, got %q", target, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("expected a directory at %q", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("expected owner-only permissions, got %o", perm)
	}

	// Re-running over an existing directory must succeed.
	if _, err := PrepareDataDir(target); err != nil {
		t.Fatalf("existing directory should be accepted: %v", err)
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDataDir(file); err == nil {
		t.Fatal("expected an error when the data dir path is a regular file")
	}
}

func TestUsageMentionsCommandsAndRoutes(t *testing.T) {
	var out strings.Builder
	Usage(&out)
	text := out.String()
	for _, want := range []string{"sql-mcp serve", "sql-mcp stdio", "--data-dir", "/healthz", "/mcp", "/sse", "/message", "/db/{id}/mcp", DefaultServeListen} {
		if !strings.Contains(text, want) {
			t.Fatalf("usage text is missing %q", want)
		}
	}
}
