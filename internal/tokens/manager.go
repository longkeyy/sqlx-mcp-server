package tokens

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// executeTool is the only tool gated by the allow_execute flag. Every other
	// tool is available to any valid credential; the database account remains
	// the real SQL privilege boundary.
	executeTool = "execute"

	maxNameRunes = 128
)

// Manager owns the token store, the administration API and HTTP
// authentication. It is safe for concurrent use; Close waits for in-flight
// operations and makes every later call fail closed with ErrClosed.
type Manager struct {
	mu         sync.RWMutex
	closed     bool
	store      *store
	legacyHash string // SHA-256 of the configured legacy AUTH_TOKEN, if any.
	handler    http.Handler
}

// principal is the authenticated identity behind a credential.
type principal struct {
	allowExecute bool
}

// validationError marks client input errors that map to HTTP 400.
type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

// Open opens (creating if needed) tokens.sqlite inside dataDir. A non-empty
// legacyToken (the historical AUTH_TOKEN setting) enables authentication
// immediately and is always allowed to call execute; it is never persisted.
func Open(dataDir, legacyToken string) (*Manager, error) {
	st, err := openStore(dataDir)
	if err != nil {
		return nil, err
	}
	m := &Manager{store: st}
	if legacy := strings.TrimSpace(legacyToken); legacy != "" {
		m.legacyHash = digest(legacy)
	}
	m.handler = m.newHandler()
	return m, nil
}

// Authorize checks the current validity of token and whether it may call tool.
// It consults the store on every call, so revocations take effect for
// already-established sessions. It returns nil, or an error wrapping
// ErrUnauthorized, ErrForbidden, ErrClosed or a storage failure (fail closed).
func (m *Manager) Authorize(token, tool string) error {
	ctx, cancel := context.WithTimeout(context.Background(), queryBudget)
	defer cancel()
	p, err := m.authenticate(ctx, token)
	if err != nil {
		return err
	}
	if isExecuteTool(tool) && !p.allowExecute {
		return fmt.Errorf("%w: token may not call %s", ErrForbidden, executeTool)
	}
	return nil
}

// Close releases the store. It is idempotent and returns nil once closed.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	if err := m.store.close(); err != nil {
		return fmt.Errorf("tokens: close store: %w", err)
	}
	return nil
}

// authenticate resolves a credential to a principal. While no authentication
// was ever configured (no legacy token, no token ever created) every caller is
// treated as the anonymous bootstrap principal, preserving legacy behavior.
func (m *Manager) authenticate(ctx context.Context, token string) (principal, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return principal{}, ErrClosed
	}

	token = strings.TrimSpace(token)
	if token != "" {
		hash := digest(token)
		if m.legacyHash != "" && subtle.ConstantTimeCompare([]byte(hash), []byte(m.legacyHash)) == 1 {
			return principal{allowExecute: true}, nil
		}
		rec, found, err := m.store.lookup(ctx, hash)
		if err != nil {
			return principal{}, err
		}
		if found {
			return principal{allowExecute: rec.AllowExecute}, nil
		}
	}

	required, err := m.authRequiredLocked(ctx)
	if err != nil {
		return principal{}, err
	}
	if !required {
		return principal{allowExecute: true}, nil
	}
	if token == "" {
		return principal{}, fmt.Errorf("%w: missing token", ErrUnauthorized)
	}
	return principal{}, fmt.Errorf("%w: invalid or revoked token", ErrUnauthorized)
}

// authRequired reports whether credentials are mandatory. On failure it
// reports true so callers fail closed.
func (m *Manager) authRequired(ctx context.Context) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return true, ErrClosed
	}
	return m.authRequiredLocked(ctx)
}

func (m *Manager) authRequiredLocked(ctx context.Context) (bool, error) {
	if m.legacyHash != "" {
		return true, nil
	}
	required, err := m.store.authRequired(ctx)
	if err != nil {
		return true, err
	}
	return required, nil
}

func (m *Manager) createToken(ctx context.Context, name string, allowExecute bool) (record, string, error) {
	name, err := normalizeName(name)
	if err != nil {
		return record{}, "", err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return record{}, "", ErrClosed
	}

	secret, err := newTokenSecret()
	if err != nil {
		return record{}, "", err
	}
	id, err := newTokenID()
	if err != nil {
		return record{}, "", err
	}
	rec := record{
		ID:           id,
		Name:         name,
		AllowExecute: allowExecute,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if err := m.store.create(ctx, rec, digest(secret)); err != nil {
		return record{}, "", err
	}
	return rec, secret, nil
}

func (m *Manager) list(ctx context.Context) ([]record, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return nil, ErrClosed
	}
	return m.store.list(ctx)
}

func (m *Manager) revokeToken(ctx context.Context, id string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return false, ErrClosed
	}
	return m.store.delete(ctx, id)
}

func isExecuteTool(tool string) bool {
	return strings.EqualFold(strings.TrimSpace(tool), executeTool)
}

func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", &validationError{msg: "name is required"}
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", &validationError{msg: fmt.Sprintf("name must be at most %d characters", maxNameRunes)}
	}
	if !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", &validationError{msg: "name must not contain control characters"}
	}
	return name, nil
}

// isValidationError reports whether err should be surfaced as a 400.
func isValidationError(err error) bool {
	var v *validationError
	return errors.As(err, &v)
}
