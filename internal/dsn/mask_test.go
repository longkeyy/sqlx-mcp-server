package dsn

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestMaskKeepsCredentialFreeDSNsReadable(t *testing.T) {
	for _, raw := range []string{
		"",
		"sqlite:///tmp/app.db",
		"sqlite://:memory:",
		"postgres://alice@prod.example:5432/app",
		"postgres://prod.example:5432/app",
	} {
		if got := Mask(raw); got != raw {
			t.Fatalf("Mask(%q) = %q, want it unchanged", raw, got)
		}
	}
}

func TestMaskHidesPassword(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "url userinfo",
			raw:  "postgres://alice:s3cr3t@prod.example:5432/app",
			want: "postgres://alice:***@prod.example:5432/app",
		},
		{
			name: "password containing an at sign",
			raw:  "mysql://u:p@ss@host:3306/db",
			want: "mysql://u:***@host:3306/db",
		},
		{
			name: "password containing a slash",
			raw:  "mysql://u:pa/ss@host/db",
			want: "mysql://u:***@host/db",
		},
		{
			name: "percent-encoded password",
			raw:  "sqlserver://sa:P%40ss@host:1433/app",
			want: "sqlserver://sa:***@host:1433/app",
		},
		{
			name: "driver connection string without scheme",
			raw:  "user:pass@tcp(host:3306)/db",
			want: "user:***@tcp(host:3306)/db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Mask(tt.raw)
			if got != tt.want {
				t.Fatalf("Mask(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			for _, fragment := range []string{"p@ss", "pa/ss", "s3cr3t", "P%40ss", ":pass@"} {
				if strings.Contains(got, fragment) {
					t.Fatalf("Mask(%q) leaks %q: %q", tt.raw, fragment, got)
				}
			}
		})
	}
}

func TestMaskHidesCredentialQueryParameters(t *testing.T) {
	raw := "clickhouse://user:pass@host:9000/events?secure=true&secret=abc123&PASSWORD=hunter2&sslmode=require"
	got := Mask(raw)

	want := "clickhouse://user:***@host:9000/events?secure=true&secret=***&PASSWORD=***&sslmode=require"
	if got != want {
		t.Fatalf("Mask() = %q, want %q", got, want)
	}
	for _, secret := range []string{"abc123", "hunter2", ":pass@"} {
		if strings.Contains(got, secret) {
			t.Fatalf("Mask() leaks %q: %q", secret, got)
		}
	}
}

func TestRedactErrorScrubsDSNPasswordAndQuerySecrets(t *testing.T) {
	const raw = "postgres://alice:s3cr3t@prod.example:5432/app?sslpassword=ssl-pass&sslmode=disable"
	cause := fmt.Errorf("dial tcp: lookup prod.example: no such host (dsn %s, sslpassword=ssl-pass)", raw)

	got := RedactError(cause, raw)
	message := got.Error()

	for _, fragment := range []string{"s3cr3t", "ssl-pass", "postgres://alice"} {
		if strings.Contains(message, fragment) {
			t.Fatalf("RedactError() leaks %q: %q", fragment, message)
		}
	}
	if !strings.Contains(message, "no such host") {
		t.Fatalf("RedactError() dropped the useful part of the message: %q", message)
	}
	if errors.Unwrap(got) != cause {
		t.Fatal("RedactError() must preserve the wrapped error")
	}
}

func TestRedactErrorScrubsEncodedPassword(t *testing.T) {
	const raw = "sqlserver://sa:P%40ss@host:1433/app"
	cause := errors.New("login failed for sa with password P%40ss")

	message := RedactError(cause, raw).Error()
	for _, fragment := range []string{"P%40ss", "P@ss"} {
		if strings.Contains(message, fragment) {
			t.Fatalf("RedactError() leaks %q: %q", fragment, message)
		}
	}
}

func TestRedactErrorReturnsOriginalWhenNothingToScrub(t *testing.T) {
	cause := errors.New("connection refused")
	if got := RedactError(cause, "postgres://alice:s3cr3t@prod.example:5432/app"); got != cause {
		t.Fatalf("expected the original error, got %v", got)
	}
	if got := RedactError(nil, "postgres://alice:s3cr3t@prod.example:5432/app"); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}
