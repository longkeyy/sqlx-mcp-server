package instances

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/longkeyy/sqlx-mcp-server/internal/dsn"
)

// errTestTimeout reports that a connection test exceeded its budget. The
// underlying driver error is discarded so a hung server cannot delay or
// pollute the response.
var errTestTimeout = errors.New("connection test timed out")

// testResponse is the body of POST /api/instances/{id}/test. The error field
// is always sanitized and never contains credentials.
type testResponse struct {
	OK        bool   `json:"ok"`
	Instance  string `json:"instance"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// serveTest probes the stored configuration with the real driver and records
// the outcome. A failed connection is a normal result (200 with ok=false),
// not an HTTP error.
func (m *Manager) serveTest(w http.ResponseWriter, r *http.Request, id string) {
	rec, err := m.get(r.Context(), id)
	if err != nil {
		m.writeError(w, r, err)
		return
	}

	timeout := m.testTimeout
	if timeout <= 0 {
		timeout = defaultTestTimeout
	}

	start := time.Now()
	probeErr := runProbe(r.Context(), rec.DSN, timeout)
	latency := time.Since(start)

	ok := probeErr == nil
	message := ""
	if !ok {
		message = sanitizeMessage(probeErr.Error(), rec.DSN)
	}

	// Persist the outcome even when the caller disconnected: the status is
	// useful to the next request. Errors here must not change the result.
	statusCtx, cancel := context.WithTimeout(context.Background(), timeout)
	if err := m.recordStatus(statusCtx, rec.ID, rec.DSN, ok, message); err != nil && !errors.Is(err, errClosed) {
		log.Printf("instances: record test status for %q: %v", rec.ID, err)
	}
	cancel()

	resp := testResponse{OK: ok, Instance: rec.ID, LatencyMS: latency.Milliseconds()}
	if !ok {
		resp.Error = message
	}
	writeJSON(w, http.StatusOK, resp)
}

// runProbe executes probeConn with a hard deadline. probeConn receives a
// cancelled context so a well-behaved driver returns promptly; the extra
// select also covers drivers that ignore cancellation.
func runProbe(ctx context.Context, rawDSN string, timeout time.Duration) error {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- probeConn(probeCtx, rawDSN) }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-done:
		return err
	case <-timer.C:
		return errTestTimeout
	case <-ctx.Done():
		return errTestTimeout
	}
}

// probeConn opens a single connection with the instance's driver, verifies it
// with a ping and a trivial query, and closes it again.
func probeConn(ctx context.Context, rawDSN string) error {
	cfg, err := dsn.Parse(rawDSN)
	if err != nil {
		// Parsing errors may echo the DSN; replace the whole message.
		return errors.New("invalid connection string")
	}

	query := "SELECT 1"
	if cfg.Driver == dsn.DriverSQLite {
		path := cfg.ConnStr
		if idx := strings.IndexByte(path, '?'); idx >= 0 {
			path = path[:idx]
		}
		if !isMemorySQLite(path) {
			if _, err := os.Stat(path); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					// Never let a connection test create a database file.
					return errors.New("sqlite database file does not exist")
				}
				return fmt.Errorf("inspect sqlite database file: %w", err)
			}
		}
		query = "SELECT count(*) FROM sqlite_master"
	}

	db, err := sql.Open(cfg.SQLDriver, cfg.ConnStr)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.PingContext(ctx); err != nil {
		return err
	}
	var n int
	if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return err
	}
	return nil
}

func isMemorySQLite(path string) bool {
	path = strings.TrimSpace(path)
	return path == ":memory:" || strings.HasPrefix(path, "file::memory:") || strings.HasPrefix(path, "file:?mode=memory")
}

const (
	maxMessageBytes = 300

	// keyValuePattern masks password-like key/value pairs in driver errors,
	// e.g. "password=hunter2" or "pwd: secret".
	keyValuePattern = `(?i)\b(password|passwd|pwd)\s*[=:]\s*[^\s,&;]*`
)

var (
	// urlUserInfoPattern masks the password of a URL-style DSN embedded in an
	// error: "postgres://user:secret@host" becomes "postgres://user:***@host".
	urlUserInfoPattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://[^/\s:@]*):[^@\s/]*@`)
	// driverUserInfoPattern masks go-sql-driver connection strings such as
	// "root:secret@tcp(db:3306)/app".
	driverUserInfoPattern = regexp.MustCompile(`([^\s:/@]+):[^@\s/]*@(tcp|unix)\(`)
	maskKeyValue          = regexp.MustCompile(keyValuePattern)
	spacePattern          = regexp.MustCompile(`\s+`)
)

// sanitizeMessage turns a raw driver error into a short, single-line message
// that is safe to return over HTTP. rawDSN supplies the credential values to
// scrub, but is never echoed itself.
func sanitizeMessage(message, rawDSN string) string {
	clean := spacePattern.ReplaceAllString(strings.TrimSpace(message), " ")
	if clean == "" {
		return "connection failed"
	}
	clean = urlUserInfoPattern.ReplaceAllString(clean, "$1:***@")
	clean = driverUserInfoPattern.ReplaceAllString(clean, "$1:***@$2(")
	clean = maskKeyValue.ReplaceAllString(clean, "$1=***")
	for _, secret := range dsnSecrets(rawDSN) {
		forms := []string{secret, url.QueryEscape(secret), url.PathEscape(secret)}
		for _, form := range forms {
			if len(form) >= 3 {
				clean = strings.ReplaceAll(clean, form, "***")
			}
		}
	}
	return truncateMessage(clean)
}

// dsnSecrets collects the credential values that may appear verbatim in a
// driver error for this DSN.
func dsnSecrets(rawDSN string) []string {
	rawDSN = strings.TrimSpace(rawDSN)
	if rawDSN == "" {
		return nil
	}
	var out []string
	add := func(v string) {
		if v == "" {
			return
		}
		for _, existing := range out {
			if existing == v {
				return
			}
		}
		out = append(out, v)
	}
	if u, err := url.Parse(rawDSN); err == nil {
		if u.User != nil {
			if pw, ok := u.User.Password(); ok {
				add(pw)
			}
		}
		for _, key := range []string{"password", "passwd", "pwd"} {
			for _, v := range u.Query()[key] {
				add(v)
			}
		}
	}
	return out
}

// truncateMessage bounds a sanitized message without splitting a rune.
func truncateMessage(msg string) string {
	if len(msg) <= maxMessageBytes {
		return msg
	}
	cut := maxMessageBytes
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut] + "…"
}

// formatUnix renders a stored unix timestamp; zero means "never".
func formatUnix(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}
