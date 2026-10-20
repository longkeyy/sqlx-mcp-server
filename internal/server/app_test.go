package server

import (
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// The startup errors below are what an operator sees in the logs, so they must
// name the database and the problem without echoing the DSN.

func TestNewAppServerRedactsInvalidDSN(t *testing.T) {
	const password = "s3cr3t"
	_, err := newAppServer(testDatabaseConfig("app", "app", "mysql://alice:"+password+"%zz@db.internal/app"))
	if err == nil {
		t.Fatal("expected a parse error")
	}

	message := err.Error()
	if !strings.Contains(message, `database "app"`) || !strings.Contains(message, "invalid DSN") {
		t.Fatalf("expected an error naming the database, got %q", message)
	}
	for _, fragment := range []string{password, "alice", "db.internal"} {
		if strings.Contains(message, fragment) {
			t.Fatalf("startup error leaks %q: %q", fragment, message)
		}
	}
}

func TestNewAppServerRedactsConnectionErrors(t *testing.T) {
	const password = "s3cr3t"
	// Port 1 on loopback refuses immediately, so this stays fast and offline.
	_, err := newAppServer(testDatabaseConfig("app", "app", "postgres://alice:"+password+"@127.0.0.1:1/app?sslmode=disable"))
	if err == nil {
		t.Fatal("expected a connection error")
	}

	message := err.Error()
	if !strings.Contains(message, `database "app" connection failed`) {
		t.Fatalf("expected a connection failure for the database, got %q", message)
	}
	for _, fragment := range []string{password, "alice"} {
		if strings.Contains(message, fragment) {
			t.Fatalf("startup error leaks %q: %q", fragment, message)
		}
	}
}
