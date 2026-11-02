package config

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestMaskDSNHidesCredentials(t *testing.T) {
	cases := []struct {
		name   string
		dsn    string
		want   string
		secret string
	}{
		{
			name:   "userinfo password",
			dsn:    "postgres://app:s3cretpw@db.internal:5432/app?sslmode=disable",
			want:   "postgres://app:***@db.internal:5432/app?sslmode=disable",
			secret: "s3cretpw",
		},
		{
			name:   "password containing at sign",
			dsn:    "mysql://app:p@ss@word@db.internal:3306/app",
			want:   "mysql://app:***@db.internal:3306/app",
			secret: "p@ss@word",
		},
		{
			name:   "password query parameter",
			dsn:    "sqlserver://db.internal:1433?database=app&user id=app&password=qp-secret",
			want:   "sqlserver://db.internal:1433?database=app&user id=app&password=***",
			secret: "qp-secret",
		},
		{
			name: "no credentials",
			dsn:  "sqlite:///var/lib/app.db",
			want: "sqlite:///var/lib/app.db",
		},
		{
			name: "user without password",
			dsn:  "clickhouse://reader@db.internal:9000/metrics",
			want: "clickhouse://reader@db.internal:9000/metrics",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MaskDSN(tc.dsn)
			if got != tc.want {
				t.Fatalf("MaskDSN(%q) = %q, want %q", tc.dsn, got, tc.want)
			}
			if tc.secret != "" && strings.Contains(got, tc.secret) {
				t.Fatalf("masked DSN still contains the secret: %q", got)
			}
		})
	}
	if MaskDSN("") != "" {
		t.Fatal("empty DSN should stay empty")
	}
}

func TestRedactDSNRemovesEchoedCredentials(t *testing.T) {
	dsn := "cockroach://app:pa%2Fss@db.internal:26257/app?password=extra-secret"
	text := `invalid DSN: parse "` + dsn + `": failed; driver echoed pa/ss and postgres://app:pa%2Fss@db.internal:26257/app and extra-secret`
	got := RedactDSN(text, dsn)
	for _, secret := range []string{"pa%2Fss", "pa/ss", "extra-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted text still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "db.internal") {
		t.Fatalf("redaction should keep non-secret context, got %s", got)
	}

	quotedDSN := `postgres://app:"quoted"@db/app`
	quotedText := "parse " + strconv.Quote(quotedDSN) + ": bad"
	if got := RedactDSN(quotedText, quotedDSN); strings.Contains(got, "quoted") {
		t.Fatalf("quoted DSN form was not redacted: %s", got)
	}

	if got := RedactDSN("connection refused", ""); got != "connection refused" {
		t.Fatalf("empty DSN should leave text unchanged, got %q", got)
	}
	if got := RedactDSN("", dsn); got != "" {
		t.Fatalf("empty text should stay empty, got %q", got)
	}
}

func TestDuplicateDatabaseErrorDoesNotLeakDSN(t *testing.T) {
	_, err := parseConfig(nil, map[string]string{
		"DB_APP_URL": "postgres://app:first-secret@db1/app",
		"DB_app_URL": "postgres://app:second-secret@db2/app",
	})
	if err == nil {
		t.Fatal("expected duplicate database id error")
	}
	for _, secret := range []string{"first-secret", "second-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("configuration error leaks a DSN secret: %v", err)
		}
	}
	if !strings.Contains(err.Error(), "DB_APP_URL") || !strings.Contains(err.Error(), "DB_app_URL") {
		t.Fatalf("configuration error should name both variables: %v", err)
	}
}

func TestEnvMapFromOS(t *testing.T) {
	t.Setenv("SQL_MCP_CONFIG_TEST_VALUE", "a=b")
	env := EnvMapFromOS()
	if env["SQL_MCP_CONFIG_TEST_VALUE"] != "a=b" {
		t.Fatalf("expected environment value with '=' preserved, got %q", env["SQL_MCP_CONFIG_TEST_VALUE"])
	}
}

func TestParseReadsProcessArguments(t *testing.T) {
	original := os.Args
	t.Cleanup(func() { os.Args = original })
	os.Args = []string{"sql-mcp", "help"}
	if _, err := Parse(); err != ErrHelp {
		t.Fatalf("expected Parse to read os.Args and return ErrHelp, got %v", err)
	}
}
