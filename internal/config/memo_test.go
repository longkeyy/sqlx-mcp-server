package config

import (
	"strings"
	"testing"
)

// The memo is rendered into MCP tool descriptions, so a multi-line or
// control-character value would break the surrounding sentence, and an
// arbitrarily long one would crowd the description. Startup normalizes it to a
// single line of at most 200 runes and warns that it did.

func TestParseConfigCollapsesMemoWhitespace(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL": "sqlite:///tmp/app.db",
		"DB_MEMO":    "  primary\napplication\tdatabase\x00  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Databases[0].Memo; got != "primary application database" {
		t.Fatalf("expected a single-line memo, got %q", got)
	}
	assertWarning(t, config.Warnings, "DB_MEMO was normalized to a single line")
}

func TestParseConfigCapsMemoAt200Runes(t *testing.T) {
	long := strings.Repeat("数", 250)
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL": "sqlite:///tmp/app.db",
		"DB_MEMO":    long,
	})
	if err != nil {
		t.Fatal(err)
	}
	memo := config.Databases[0].Memo
	if runes := []rune(memo); len(runes) != 200 || runes[199] != '…' {
		t.Fatalf("expected 200 runes ending in an ellipsis, got %d runes: %q", len(runes), memo)
	}
	if !strings.HasPrefix(memo, strings.Repeat("数", 199)) {
		t.Fatalf("expected the memo to be cut on a rune boundary, got %q", memo)
	}
	assertWarning(t, config.Warnings, "DB_MEMO was truncated to 200 characters")
}

func TestParseConfigSanitizesPerIDMemo(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":  "sqlite:///tmp/app.db",
		"DB_APP_MEMO": "line one\nline two",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Databases[0].Memo; got != "line one line two" {
		t.Fatalf("expected the per-id memo to collapse to one line, got %q", got)
	}
	assertWarning(t, config.Warnings, "DB_APP_MEMO was normalized to a single line")
}

func TestParseConfigLeavesCleanMemoAlone(t *testing.T) {
	config, err := parseConfig(nil, map[string]string{
		"DB_APP_URL":  "sqlite:///tmp/app.db",
		"DB_APP_MEMO": "Primary application database",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Databases[0].Memo; got != "Primary application database" {
		t.Fatalf("expected the memo to be unchanged, got %q", got)
	}
	if len(config.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %q", config.Warnings)
	}
}
