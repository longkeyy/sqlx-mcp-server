package config

import (
	"strings"
	"testing"
)

func TestMaskDSNDelegatesToTheDSNMasker(t *testing.T) {
	masked := MaskDSN("postgres://alice:s3cr3t@prod.example:5432/app?sslmode=disable")
	if masked != "postgres://alice:***@prod.example:5432/app?sslmode=disable" {
		t.Fatalf("unexpected masked DSN %q", masked)
	}
	if strings.Contains(masked, "s3cr3t") {
		t.Fatalf("MaskDSN leaked the password: %q", masked)
	}
	if MaskDSN("") != "" {
		t.Fatal("expected an empty DSN to stay empty")
	}
}
