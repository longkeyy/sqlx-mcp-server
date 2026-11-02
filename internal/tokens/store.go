package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Registers the "sqlite" driver.
)

const (
	// storeFileName is the token database kept inside the data directory so it
	// never shares migration ownership with other modules.
	storeFileName = "tokens.sqlite"
	schemaVersion = 1

	settingAuthEnabled = "auth_enabled"
	migrationBudget    = 10 * time.Second
	queryBudget        = 5 * time.Second
)

// record is the non-secret metadata kept for each token. The plaintext token
// and its digest never leave the store package.
type record struct {
	ID           string
	Name         string
	AllowExecute bool
	CreatedAt    string
}

type store struct {
	db *sql.DB
}

func openStore(dataDir string) (*store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("tokens: data directory must not be empty")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("tokens: create data directory: %w", err)
	}
	path := filepath.Join(dataDir, storeFileName)
	if err := ensureStoreFile(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", storeDSN(path))
	if err != nil {
		return nil, fmt.Errorf("tokens: open store: %w", err)
	}
	// SQLite serializes writers itself; a small pool allows concurrent readers
	// while the busy timeout absorbs cross-process contention.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	ctx, cancel := context.WithTimeout(context.Background(), migrationBudget)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("tokens: open store: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

// ensureStoreFile creates the database file with owner-only permissions before
// SQLite can create it itself with a wider umask.
func ensureStoreFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("tokens: create store file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("tokens: create store file: %w", err)
	}
	// Tighten a file left behind by a previous version or a wider umask.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("tokens: restrict store permissions: %w", err)
	}
	return nil
}

// storeDSN builds a file: URI so path characters such as '?', '#' and '%' are
// escaped instead of being parsed as SQLite connection parameters.
func storeDSN(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.ToSlash(abs)
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs // url.URL.Path must be rooted to produce file:///...
	}
	u := url.URL{Scheme: "file", Path: abs}
	params := url.Values{}
	params.Add("_pragma", "busy_timeout(5000)")
	params.Add("_pragma", "secure_delete(1)")
	params.Set("_txlock", "immediate")
	u.RawQuery = params.Encode()
	return u.String()
}

func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("tokens: begin migration: %w", err)
	}
	defer tx.Rollback()

	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("tokens: read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("tokens: store schema version %d is newer than supported version %d", version, schemaVersion)
	}
	if version < 1 {
		stmts := []string{
			`CREATE TABLE tokens (
				seq           INTEGER PRIMARY KEY,
				id            TEXT    NOT NULL UNIQUE,
				name          TEXT    NOT NULL,
				token_hash    TEXT    NOT NULL UNIQUE,
				allow_execute INTEGER NOT NULL DEFAULT 0 CHECK (allow_execute IN (0, 1)),
				created_at    TEXT    NOT NULL
			)`,
			`CREATE TABLE settings (
				key   TEXT NOT NULL PRIMARY KEY,
				value TEXT NOT NULL
			)`,
			`PRAGMA user_version = 1`,
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("tokens: migrate store: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("tokens: commit migration: %w", err)
	}
	return nil
}

func (s *store) close() error {
	return s.db.Close()
}

// authRequired reports whether credentials are mandatory. The policy is sticky:
// it is persisted when the first API token is created so revoking every token
// never reopens anonymous access.
func (s *store) authRequired(ctx context.Context) (bool, error) {
	var enabled bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM tokens)
		    OR EXISTS(SELECT 1 FROM settings WHERE key = ? AND value = '1')`,
		settingAuthEnabled).Scan(&enabled)
	if err != nil {
		return false, fmt.Errorf("tokens: read auth policy: %w", err)
	}
	return enabled, nil
}

func (s *store) lookup(ctx context.Context, tokenHash string) (record, bool, error) {
	rec := record{}
	var allowExecute int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, allow_execute, created_at FROM tokens WHERE token_hash = ?`,
		tokenHash).Scan(&rec.ID, &rec.Name, &allowExecute, &rec.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return record{}, false, nil
	}
	if err != nil {
		return record{}, false, fmt.Errorf("tokens: look up token: %w", err)
	}
	rec.AllowExecute = allowExecute == 1
	return rec, true, nil
}

func (s *store) create(ctx context.Context, rec record, tokenHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("tokens: begin token creation: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tokens (id, name, token_hash, allow_execute, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		rec.ID, rec.Name, tokenHash, boolToInt(rec.AllowExecute), rec.CreatedAt); err != nil {
		return fmt.Errorf("tokens: insert token: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, '1')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		settingAuthEnabled); err != nil {
		return fmt.Errorf("tokens: persist auth policy: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("tokens: commit token creation: %w", err)
	}
	return nil
}

func (s *store) list(ctx context.Context) ([]record, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, allow_execute, created_at FROM tokens ORDER BY seq`)
	if err != nil {
		return nil, fmt.Errorf("tokens: list tokens: %w", err)
	}
	defer rows.Close()

	records := make([]record, 0)
	for rows.Next() {
		rec := record{}
		var allowExecute int
		if err := rows.Scan(&rec.ID, &rec.Name, &allowExecute, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("tokens: scan token: %w", err)
		}
		rec.AllowExecute = allowExecute == 1
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tokens: list tokens: %w", err)
	}
	return records, nil
}

func (s *store) delete(ctx context.Context, id string) (bool, error) {
	// Writes always run in a BEGIN IMMEDIATE transaction (see _txlock) so the
	// busy handler applies instead of SQLite's lock-upgrade deadlock shortcut.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("tokens: begin revocation: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM tokens WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("tokens: revoke token: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("tokens: revoke token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("tokens: commit revocation: %w", err)
	}
	return affected > 0, nil
}

// newTokenSecret returns a URL-safe high-entropy bearer token. The prefix makes
// leaked credentials easy to recognize without weakening entropy.
func newTokenSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("tokens: generate secret: %w", err)
	}
	return "sqlmcp_" + hex.EncodeToString(buf), nil
}

func newTokenID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("tokens: generate id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// digest hashes a token for storage and comparison. Tokens carry full entropy,
// so a plain SHA-256 is sufficient and keeps constant-time lookups simple.
func digest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
