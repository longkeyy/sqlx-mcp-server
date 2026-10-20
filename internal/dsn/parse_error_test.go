package dsn

import (
	"strings"
	"testing"
)

func TestParseErrorDoesNotEchoTheDSN(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		secrets []string
	}{
		{
			name:    "invalid escape in password",
			raw:     "postgres://alice:s3cr%zzt@prod.example:5432/app",
			secrets: []string{"s3cr", "alice", "prod.example"},
		},
		{
			name:    "space in password",
			raw:     "mysql://bob:hunter 2@db.internal:3306/app",
			secrets: []string{"hunter", "db.internal"},
		},
		{
			name:    "unescaped slash in password",
			raw:     "mysql://carol:pa/ss@db.internal/app",
			secrets: []string{"pa/ss", ":pa", "carol"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.raw)
			if err == nil {
				t.Fatalf("expected Parse(%q) to fail", tt.raw)
			}
			message := err.Error()
			if !strings.Contains(message, "invalid DSN") {
				t.Fatalf("expected an invalid DSN error, got %q", message)
			}
			for _, secret := range tt.secrets {
				if strings.Contains(message, secret) {
					t.Fatalf("error leaks %q: %q", secret, message)
				}
			}
		})
	}
}
