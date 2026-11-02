package routing

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/longkeyy/sqlx-mcp-server/internal/config"
	sqlmcp "github.com/longkeyy/sqlx-mcp-server/internal/server"
)

// instanceEntry owns one live connection pool for one registry snapshot. It is
// reference counted so a pool replaced by a DSN update stays open until the
// calls that still target it have finished.
type instanceEntry struct {
	cfg         config.DatabaseConfig
	fingerprint string
	app         *sqlmcp.AppServer
	refs        int
	retired     bool
	closed      bool
}

// poolBuild deduplicates concurrent pool creations for the same instance so a
// burst of first calls opens exactly one pool.
type poolBuild struct {
	fingerprint string
	done        chan struct{}
	entry       *instanceEntry
	err         error
}

// acquire returns a live pool for cfg, rebuilding it when the registry's
// configuration for that instance changed. The returned entry must be released
// when the call finishes.
func (r *Router) acquire(ctx context.Context, cfg config.DatabaseConfig) (*instanceEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.ID) == "" {
		return nil, errors.New("routing: registry returned a configuration without an ID")
	}
	fingerprint := configFingerprint(cfg)

	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, ErrClosed
		}
		if entry := r.current[cfg.ID]; entry != nil && !entry.retired && entry.fingerprint == fingerprint {
			entry.refs++
			r.mu.Unlock()
			return entry, nil
		}
		if build := r.building[cfg.ID]; build != nil {
			if build.fingerprint == fingerprint {
				done := build.done
				r.mu.Unlock()
				select {
				case <-done:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				if build.err != nil {
					return nil, build.err
				}
				// The builder installed (or closed) its entry; look again so an
				// entry that was retired meanwhile is rebuilt instead of reused.
				continue
			}
			// A different configuration is being built. Wait for it to finish so
			// the newest registry snapshot wins, then re-evaluate.
			done := build.done
			r.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			continue
		}
		build := &poolBuild{fingerprint: fingerprint, done: make(chan struct{})}
		r.building[cfg.ID] = build
		r.mu.Unlock()

		r.buildPool(cfg, build)
		if build.err != nil {
			return nil, build.err
		}
		if build.entry == nil {
			return nil, ErrClosed
		}
		return build.entry, nil
	}
}

// buildPool creates the pool for one (id, fingerprint) and publishes it. It is
// the only place that talks to internal/server, and it never connects under the
// router lock.
func (r *Router) buildPool(cfg config.DatabaseConfig, build *poolBuild) {
	var app *sqlmcp.AppServer
	fail := func(err error) {
		build.err = err
		r.mu.Lock()
		if r.building[cfg.ID] == build {
			delete(r.building, cfg.ID)
		}
		r.mu.Unlock()
		close(build.done)
	}

	apps, err := sqlmcp.BuildAppServers([]config.DatabaseConfig{cfg})
	if err != nil {
		fail(fmt.Errorf("routing: instance %q: %w", cfg.ID, err))
		return
	}
	app = apps[cfg.ID]
	if app == nil {
		sqlmcp.CloseAppServers(apps)
		fail(fmt.Errorf("routing: instance %q: no server was built", cfg.ID))
		return
	}

	entry := &instanceEntry{
		cfg:         cfg,
		fingerprint: build.fingerprint,
		app:         app,
		refs:        1,
	}

	r.mu.Lock()
	if r.building[cfg.ID] == build {
		delete(r.building, cfg.ID)
	}
	if r.closed {
		r.mu.Unlock()
		entry.closed = true
		closeEntryApp(entry)
		build.err = ErrClosed
		close(build.done)
		return
	}
	// A racing build with the same fingerprint may have won while this one was
	// connecting. Reuse the published pool instead of opening a second one.
	if existing := r.current[cfg.ID]; existing != nil && !existing.retired && existing.fingerprint == build.fingerprint {
		existing.refs++
		r.mu.Unlock()
		entry.closed = true
		closeEntryApp(entry)
		build.entry = existing
		close(build.done)
		return
	}
	previous := r.current[cfg.ID]
	r.current[cfg.ID] = entry
	var closeNow *instanceEntry
	if previous != nil {
		previous.retired = true
		if previous.refs == 0 {
			closeNow = previous
		} else {
			r.retired = append(r.retired, previous)
		}
	}
	build.entry = entry
	r.mu.Unlock()

	if closeNow != nil {
		closeEntryApp(closeNow)
	}
	close(build.done)
}

// release drops a reference. A retired entry is closed once its last in-flight
// call finishes.
func (r *Router) release(entry *instanceEntry) {
	if entry == nil {
		return
	}
	r.mu.Lock()
	if entry.refs > 0 {
		entry.refs--
	}
	var closeNow *instanceEntry
	if entry.refs == 0 && entry.retired && !entry.closed {
		for i, retired := range r.retired {
			if retired == entry {
				r.retired = append(r.retired[:i], r.retired[i+1:]...)
				break
			}
		}
		entry.closed = true
		closeNow = entry
	}
	r.mu.Unlock()
	if closeNow != nil {
		closeEntryApp(closeNow)
	}
}

// forget drops the cached pool for id. It is called when the registry no longer
// resolves the instance so a deleted-and-recreated instance does not keep
// serving from a stale connection. Idle pools close immediately; pools with
// in-flight calls are closed by the last release. Unknown IDs are ignored.
func (r *Router) forget(id string) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	entry := r.current[id]
	if entry == nil {
		r.mu.Unlock()
		return nil
	}
	delete(r.current, id)
	var closeNow *instanceEntry
	entry.retired = true
	if entry.refs == 0 && !entry.closed {
		entry.closed = true
		closeNow = entry
	} else if !entry.closed {
		r.retired = append(r.retired, entry)
	}
	r.mu.Unlock()

	if closeNow != nil {
		closeEntryApp(closeNow)
	}
	return nil
}

func closeEntryApp(entry *instanceEntry) {
	if entry == nil || entry.app == nil {
		return
	}
	sqlmcp.CloseAppServers(map[string]*sqlmcp.AppServer{entry.cfg.ID: entry.app})
}

func configFingerprint(cfg config.DatabaseConfig) string {
	return strings.Join([]string{
		cfg.ID,
		cfg.Name,
		cfg.Memo,
		cfg.DSN,
		strconv.Itoa(cfg.MaxOpenConns),
		strconv.Itoa(cfg.MaxIdleConns),
	}, "\x00")
}
