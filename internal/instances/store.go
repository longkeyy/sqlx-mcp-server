package instances

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
)

// storeParams configures every pooled connection to the management store:
// wait for locks held by other processes, use WAL so readers never block
// the writer, fsync each commit, and take the write lock when a transaction
// begins so read-then-write transactions cannot fail with SQLITE_BUSY on
// upgrade. busy_timeout comes first so the remaining pragmas also wait.
const storeParams = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_txlock=immediate"

const schemaVersion = 1

var schemaV1 = []string{
	`CREATE TABLE instances (
		id             TEXT PRIMARY KEY,
		name           TEXT NOT NULL,
		memo           TEXT NOT NULL DEFAULT '',
		dsn            TEXT NOT NULL,
		max_open_conns INTEGER NOT NULL DEFAULT 25,
		max_idle_conns INTEGER NOT NULL DEFAULT 1,
		status         TEXT NOT NULL DEFAULT 'unknown',
		status_error   TEXT,
		tested_at      INTEGER,
		created_at     INTEGER NOT NULL,
		updated_at     INTEGER NOT NULL
	)`,
	`CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
}

const settingDefaultInstance = "default_instance"

// migrate brings the schema to schemaVersion inside one immediate
// transaction, so processes opening the same store concurrently serialise.
func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("store schema version %d is newer than supported version %d", version, schemaVersion)
	}
	if version < 1 {
		for _, stmt := range schemaV1 {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// record is the persisted form of an instance.
type record struct {
	ID           string
	Name         string
	Memo         string
	DSN          string
	MaxOpenConns int
	MaxIdleConns int
	Status       string
	StatusError  string
	TestedAt     int64 // unix seconds; 0 means never tested
	CreatedAt    int64
	UpdatedAt    int64
	IsDefault    bool
}

func (r record) config() config.DatabaseConfig {
	maxOpen, maxIdle := r.MaxOpenConns, r.MaxIdleConns
	if maxOpen <= 0 {
		maxOpen = defaultMaxOpenConns
	}
	if maxIdle <= 0 {
		maxIdle = defaultMaxIdleConns
	}
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}
	return config.DatabaseConfig{
		ID:           r.ID,
		Name:         r.Name,
		Memo:         r.Memo,
		DSN:          r.DSN,
		MaxOpenConns: maxOpen,
		MaxIdleConns: maxIdle,
	}
}

// queryer is satisfied by *sql.DB and *sql.Tx.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const selectRecords = `
SELECT i.id, i.name, i.memo, i.dsn, i.max_open_conns, i.max_idle_conns,
       i.status, COALESCE(i.status_error, ''), COALESCE(i.tested_at, 0),
       i.created_at, i.updated_at, s.value IS NOT NULL
FROM instances i
LEFT JOIN settings s ON s.key = 'default_instance' AND s.value = i.id`

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(row scanner) (record, error) {
	var rec record
	err := row.Scan(&rec.ID, &rec.Name, &rec.Memo, &rec.DSN, &rec.MaxOpenConns, &rec.MaxIdleConns,
		&rec.Status, &rec.StatusError, &rec.TestedAt, &rec.CreatedAt, &rec.UpdatedAt, &rec.IsDefault)
	return rec, err
}

func listRecords(ctx context.Context, q queryer) ([]record, error) {
	rows, err := q.QueryContext(ctx, selectRecords+` ORDER BY i.id`)
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	defer rows.Close()

	records := []record{}
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("list instances: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	return records, nil
}

func getRecord(ctx context.Context, q queryer, id string) (record, error) {
	if id == "" {
		return record{}, notFound(id)
	}
	rec, err := scanRecord(q.QueryRowContext(ctx, selectRecords+` WHERE i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return record{}, notFound(id)
	}
	if err != nil {
		return record{}, fmt.Errorf("read instance: %w", err)
	}
	return rec, nil
}

func readDefault(ctx context.Context, q queryer) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `
SELECT i.id FROM settings s JOIN instances i ON i.id = s.value
WHERE s.key = 'default_instance'`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read default instance: %w", err)
	}
	return id, nil
}

// inTx runs fn in an immediate transaction (see storeParams).
func (m *Manager) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// create persists a validated new instance; duplicate IDs yield errConflict.
func (m *Manager) create(ctx context.Context, in instanceInput) (record, error) {
	release, err := m.acquireWrite()
	if err != nil {
		return record{}, err
	}
	defer release()

	now := m.now().Unix()
	var rec record
	err = m.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
INSERT INTO instances (id, name, memo, dsn, max_open_conns, max_idle_conns, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`,
			in.ID, in.Name, in.Memo, in.DSN, defaultMaxOpenConns, defaultMaxIdleConns, statusUnknown, now, now)
		if err != nil {
			return fmt.Errorf("insert instance: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("insert instance: %w", err)
		}
		if n == 0 {
			return errConflict
		}
		rec, err = getRecord(ctx, tx, in.ID)
		return err
	})
	return rec, err
}

// instancePatch carries validated optional updates; nil leaves a field as is.
type instancePatch struct {
	Name *string
	Memo *string
	DSN  *string
}

// update applies p to instance id. The ID never changes; replacing the DSN
// resets the recorded connection status.
func (m *Manager) update(ctx context.Context, id string, p instancePatch) (record, error) {
	release, err := m.acquireWrite()
	if err != nil {
		return record{}, err
	}
	defer release()

	var rec record
	err = m.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := getRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		changed := false
		if p.Name != nil && *p.Name != cur.Name {
			cur.Name, changed = *p.Name, true
		}
		if p.Memo != nil && *p.Memo != cur.Memo {
			cur.Memo, changed = *p.Memo, true
		}
		if p.DSN != nil && *p.DSN != cur.DSN {
			cur.DSN, changed = *p.DSN, true
			cur.Status, cur.StatusError, cur.TestedAt = statusUnknown, "", 0
		}
		if !changed {
			rec = cur
			return nil
		}
		cur.UpdatedAt = m.now().Unix()
		if _, err := tx.ExecContext(ctx, `
UPDATE instances
SET name = ?, memo = ?, dsn = ?, status = ?, status_error = NULLIF(?, ''), tested_at = NULLIF(?, 0), updated_at = ?
WHERE id = ?`,
			cur.Name, cur.Memo, cur.DSN, cur.Status, cur.StatusError, cur.TestedAt, cur.UpdatedAt, cur.ID); err != nil {
			return fmt.Errorf("update instance: %w", err)
		}
		rec = cur
		return nil
	})
	return rec, err
}

// remove deletes instance id and clears it as default in the same
// transaction; no other instance is promoted.
func (m *Manager) remove(ctx context.Context, id string) error {
	release, err := m.acquireWrite()
	if err != nil {
		return err
	}
	defer release()

	return m.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM instances WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete instance: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete instance: %w", err)
		}
		if n == 0 {
			return notFound(id)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ? AND value = ?`, settingDefaultInstance, id); err != nil {
			return fmt.Errorf("clear default instance: %w", err)
		}
		return nil
	})
}

// setDefault persists id as the default instance; id must exist.
func (m *Manager) setDefault(ctx context.Context, id string) error {
	release, err := m.acquireWrite()
	if err != nil {
		return err
	}
	defer release()

	return m.inTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM instances WHERE id = ?)`, id).Scan(&exists); err != nil {
			return fmt.Errorf("read instance: %w", err)
		}
		if !exists {
			return notFound(id)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO settings (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, settingDefaultInstance, id); err != nil {
			return fmt.Errorf("set default instance: %w", err)
		}
		return nil
	})
}

// get returns the stored record for id.
func (m *Manager) get(ctx context.Context, id string) (record, error) {
	release, err := m.acquire()
	if err != nil {
		return record{}, err
	}
	defer release()
	return getRecord(ctx, m.db, id)
}

// records returns every stored record ordered by ID.
func (m *Manager) records(ctx context.Context) ([]record, error) {
	release, err := m.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	return listRecords(ctx, m.db)
}

// recordStatus stores a connection-test outcome. The DSN guard drops results
// for a configuration that was replaced while the test was running.
func (m *Manager) recordStatus(ctx context.Context, id, testedDSN string, ok bool, message string) error {
	release, err := m.acquireWrite()
	if err != nil {
		return err
	}
	defer release()

	status := statusOK
	if !ok {
		status = statusError
	}
	_, err = m.db.ExecContext(ctx, `
UPDATE instances SET status = ?, status_error = NULLIF(?, ''), tested_at = ?
WHERE id = ? AND dsn = ?`, status, message, m.now().Unix(), id, testedDSN)
	if err != nil {
		return fmt.Errorf("record instance status: %w", err)
	}
	return nil
}
