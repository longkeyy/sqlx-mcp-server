package instances

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
)

const (
	// storeFileName is the management store created inside the data directory.
	storeFileName = "instances.sqlite"

	// Pool defaults mirror internal/config so managed instances behave like
	// environment-configured ones.
	defaultMaxOpenConns = 25
	defaultMaxIdleConns = 1

	// Instance status values exposed by the HTTP API.
	statusUnknown = "unknown"
	statusOK      = "available"
	statusError   = "unavailable"

	// sourceManaged marks instances persisted by this module, as opposed to
	// environment/CLI configuration overlaid by the integration layer.
	sourceManaged = "managed"

	defaultTestTimeout = 5 * time.Second
)

var (
	errClosed   = errors.New("instance store is closed")
	errNotFound = errors.New("instance not found")
	errConflict = errors.New("instance already exists")
)

// Manager is a concurrency-safe registry of database instances persisted in
// an SQLite store inside the data directory.
//
// Every read goes to the store, so changes made through another Manager or
// another process sharing the data directory are visible at the next
// operation boundary. List and Resolve return trusted configuration
// (including DSNs) for in-process consumers; the HTTP handler never exposes
// DSNs or credentials.
type Manager struct {
	db       *sql.DB
	external map[string]config.DatabaseConfig

	// mu guards closed. Operations hold the read lock for their whole
	// duration so Close waits for in-flight store access.
	mu     sync.RWMutex
	closed bool

	// writeMu serialises writes issued by this process. Lock order is
	// writeMu before mu; Close only takes mu, so no cycle exists. Immediate
	// transactions plus SQLite's busy timeout serialise writers across
	// processes.
	writeMu sync.Mutex

	// testTimeout bounds a single connection test.
	testTimeout time.Duration
	now         func() time.Time
}

// Open opens (creating if necessary) the instance store at
// dataDir/instances.sqlite. The directory is created with mode 0700 and the
// store file is restricted to mode 0600.
func Open(dataDir string) (*Manager, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("instances: data directory is required")
	}
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("instances: resolve data directory: %w", err)
	}
	// The SQLite driver treats '?' as the start of connection parameters.
	if strings.Contains(dir, "?") {
		return nil, errors.New("instances: data directory path must not contain '?'")
	}
	if err := ensureDataDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, storeFileName)
	if err := ensureStoreFile(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path+storeParams)
	if err != nil {
		return nil, fmt.Errorf("instances: open store: %w", err)
	}
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("instances: open store: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("instances: migrate store: %w", err)
	}
	return &Manager{
		db:          db,
		testTimeout: defaultTestTimeout,
		now:         func() time.Time { return time.Now().UTC() },
	}, nil
}

// ensureDataDir creates dir with mode 0700 when missing. Existing
// directories keep their mode because they may be shared with other modules.
func ensureDataDir(dir string) error {
	info, err := os.Stat(dir)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("instances: data directory %q is not a directory", dir)
		}
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("instances: create data directory: %w", err)
		}
		// MkdirAll is subject to the umask; enforce the documented mode.
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("instances: restrict data directory: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("instances: inspect data directory: %w", err)
	}
}

// ensureStoreFile creates the store file with mode 0600 before SQLite opens
// it, and tightens the mode of an existing store.
func ensureStoreFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("instances: open store file: %w", err)
	}
	info, statErr := f.Stat()
	closeErr := f.Close()
	if statErr != nil {
		return fmt.Errorf("instances: inspect store file: %w", statErr)
	}
	if closeErr != nil {
		return fmt.Errorf("instances: close store file: %w", closeErr)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("instances: store %q is not a regular file", path)
	}
	if info.Mode().Perm() != 0o600 {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("instances: restrict store file: %w", err)
		}
	}
	return nil
}

// Close releases the store. It waits for in-flight operations, is
// idempotent, and makes later operations fail with errClosed.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	return m.db.Close()
}

// List returns every managed instance ordered by ID. The slice is never nil.
func (m *Manager) List(ctx context.Context) ([]config.DatabaseConfig, error) {
	release, err := m.acquire()
	if err != nil {
		return nil, err
	}
	defer release()

	records, err := listRecords(ctx, m.db)
	if err != nil {
		return nil, err
	}
	out := make([]config.DatabaseConfig, 0, len(records))
	for _, rec := range records {
		if _, exists := m.external[rec.ID]; !exists {
			out = append(out, rec.config())
		}
	}
	for _, c := range m.external {
		out = append(out, c)
	}
	sortConfigs(out)
	return out, nil
}

// Resolve returns the instance whose ID matches id exactly. Unknown IDs are
// an error; there is no fallback to the default or any other instance.
func (m *Manager) Resolve(ctx context.Context, id string) (config.DatabaseConfig, error) {
	release, err := m.acquire()
	if err != nil {
		return config.DatabaseConfig{}, err
	}
	defer release()
	if c, ok := m.external[id]; ok {
		return c, nil
	}

	rec, err := getRecord(ctx, m.db, id)
	if err != nil {
		return config.DatabaseConfig{}, err
	}
	return rec.config(), nil
}

// Default returns the persisted default instance ID, or "" when unset.
func (m *Manager) Default(ctx context.Context) (string, error) {
	release, err := m.acquire()
	if err != nil {
		return "", err
	}
	defer release()
	var id string
	err = m.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='default_instance'").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// acquire takes the shared lock for a read operation.
func (m *Manager) acquire() (func(), error) {
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return nil, errClosed
	}
	return m.mu.RUnlock, nil
}

// acquireWrite serialises an in-process write and takes the shared lock.
func (m *Manager) acquireWrite() (func(), error) {
	m.writeMu.Lock()
	release, err := m.acquire()
	if err != nil {
		m.writeMu.Unlock()
		return nil, err
	}
	return func() {
		release()
		m.writeMu.Unlock()
	}, nil
}

func notFound(id string) error {
	return fmt.Errorf("%w: %q", errNotFound, id)
}
