package tokens

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTestManager(t *testing.T, legacyToken string) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	manager, err := Open(dir, legacyToken)
	if err != nil {
		t.Fatalf("Open(%q): %v", dir, err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager, dir
}

func mustCreateToken(t *testing.T, manager *Manager, name string, allowExecute bool) (record, string) {
	t.Helper()
	rec, secret, err := manager.createToken(context.Background(), name, allowExecute)
	if err != nil {
		t.Fatalf("createToken(%q): %v", name, err)
	}
	return rec, secret
}

func TestOpenBootstrapAllowsAnonymousAccessUntilFirstToken(t *testing.T) {
	manager, _ := openTestManager(t, "")

	for _, tool := range []string{"query", "list_objects", "describe_object", "execute"} {
		if err := manager.Authorize("", tool); err != nil {
			t.Fatalf("fresh installation must allow anonymous %s: %v", tool, err)
		}
	}

	_, reader := mustCreateToken(t, manager, "reader", false)

	if err := manager.Authorize("", "query"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("anonymous access must stop after first token, got %v", err)
	}
	if err := manager.Authorize("invalid", "query"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown token must be rejected, got %v", err)
	}
	if err := manager.Authorize(reader, "query"); err != nil {
		t.Fatalf("reader query denied: %v", err)
	}
	if err := manager.Authorize(reader, "list_objects"); err != nil {
		t.Fatalf("reader list_objects denied: %v", err)
	}
	if err := manager.Authorize(reader, "execute"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reader execute must be forbidden, got %v", err)
	}
	if err := manager.Authorize(reader, "EXECUTE"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("execute check must be case-insensitive, got %v", err)
	}
	if err := manager.Authorize(" "+reader+" ", "query"); err != nil {
		t.Fatalf("surrounding whitespace should be tolerated: %v", err)
	}

	writerRec, writer := mustCreateToken(t, manager, "writer", true)
	if writer == reader {
		t.Fatal("generated tokens must be distinct")
	}
	if writerRec.ID == "" || writerRec.Name != "writer" || !writerRec.AllowExecute {
		t.Fatalf("unexpected record: %+v", writerRec)
	}
	if err := manager.Authorize(writer, "execute"); err != nil {
		t.Fatalf("writer execute denied: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, writerRec.CreatedAt); err != nil {
		t.Fatalf("created_at is not RFC3339: %q: %v", writerRec.CreatedAt, err)
	}
}

func TestRevocationIsImmediateAndPersistent(t *testing.T) {
	manager, dir := openTestManager(t, "")
	readerRec, reader := mustCreateToken(t, manager, "reader", false)
	writerRec, writer := mustCreateToken(t, manager, "writer", true)

	removed, err := manager.revokeToken(context.Background(), writerRec.ID)
	if err != nil || !removed {
		t.Fatalf("revoke writer: removed=%v err=%v", removed, err)
	}
	if removed, err := manager.revokeToken(context.Background(), writerRec.ID); err != nil || removed {
		t.Fatalf("second revoke should report not found: removed=%v err=%v", removed, err)
	}
	if err := manager.Authorize(writer, "execute"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked token must be denied immediately, got %v", err)
	}
	if err := manager.Authorize(reader, "query"); err != nil {
		t.Fatalf("reader must survive writer revocation: %v", err)
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dir, "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if err := reopened.Authorize(reader, "query"); err != nil {
		t.Fatalf("reader must be persisted: %v", err)
	}
	if err := reopened.Authorize(writer, "execute"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked writer must stay revoked after restart, got %v", err)
	}

	if removed, err := reopened.revokeToken(context.Background(), readerRec.ID); err != nil || !removed {
		t.Fatalf("revoke last reader: removed=%v err=%v", removed, err)
	}
	if err := reopened.Authorize("", "query"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("authentication must stay enabled after revoking the last token, got %v", err)
	}

	if err := reopened.Close(); err != nil {
		t.Fatalf("Close reopened: %v", err)
	}
	third, err := Open(dir, "")
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	defer third.Close()
	if err := third.Authorize("", "query"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("persisted auth policy lost after restart, got %v", err)
	}
	if err := third.Authorize(reader, "query"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked reader resurrected after restart, got %v", err)
	}
}

func TestLegacyTokenIsExecuteEnabled(t *testing.T) {
	manager, _ := openTestManager(t, "  legacy-secret  ")

	for _, tool := range []string{"query", "list_objects", "execute"} {
		if err := manager.Authorize("legacy-secret", tool); err != nil {
			t.Fatalf("legacy token denied %s: %v", tool, err)
		}
	}
	if err := manager.Authorize("", "query"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("legacy token must enable authentication, got %v", err)
	}
	if err := manager.Authorize("wrong", "query"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong token accepted, got %v", err)
	}

	rec, secret := mustCreateToken(t, manager, "reader", false)
	if err := manager.Authorize(secret, "query"); err != nil {
		t.Fatalf("API token must work alongside legacy token: %v", err)
	}
	if err := manager.Authorize(secret, "execute"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("API tokens keep their own execute flag, got %v", err)
	}
	if removed, err := manager.revokeToken(context.Background(), rec.ID); err != nil || !removed {
		t.Fatalf("revoke: removed=%v err=%v", removed, err)
	}
	if err := manager.Authorize(secret, "query"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked API token still accepted, got %v", err)
	}
	if err := manager.Authorize("legacy-secret", "execute"); err != nil {
		t.Fatalf("legacy token must survive API token lifecycle: %v", err)
	}
}

func TestStoreNeverContainsPlaintextTokens(t *testing.T) {
	manager, dir := openTestManager(t, "")
	_, reader := mustCreateToken(t, manager, "reader", false)
	_, writer := mustCreateToken(t, manager, "writer", true)
	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(data)
		if strings.Contains(text, reader) || strings.Contains(text, writer) {
			t.Errorf("plaintext token persisted in %s", entry.Name())
		}
		if strings.Contains(text, digest(reader)) && entry.Name() != storeFileName {
			t.Errorf("token digest leaked into unexpected file %s", entry.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk data dir: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, storeFileName))
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("store permissions too open: %o", mode)
	}
}

func TestOpenEncodesSpecialCharactersInDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data?with#specials%and spaces")
	manager, err := Open(dir, "")
	if err != nil {
		t.Fatalf("Open with special characters: %v", err)
	}
	_, secret := mustCreateToken(t, manager, "reader", false)
	if err := manager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, storeFileName)); err != nil {
		t.Fatalf("store file missing in data dir: %v", err)
	}
	reopened, err := Open(dir, "")
	if err != nil {
		t.Fatalf("reopen with special characters: %v", err)
	}
	defer reopened.Close()
	if err := reopened.Authorize(secret, "query"); err != nil {
		t.Fatalf("token not persisted in escaped data dir: %v", err)
	}
}

func TestCloseIsIdempotentAndFailsClosed(t *testing.T) {
	manager, _ := openTestManager(t, "")
	_, secret := mustCreateToken(t, manager, "reader", false)

	if err := manager.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second Close must be a no-op: %v", err)
	}
	if err := manager.Authorize(secret, "query"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Authorize after Close must fail closed, got %v", err)
	}
	if _, _, err := manager.createToken(context.Background(), "late", false); !errors.Is(err, ErrClosed) {
		t.Fatalf("create after Close must fail, got %v", err)
	}
	if _, err := manager.list(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("list after Close must fail, got %v", err)
	}
	if _, err := manager.revokeToken(context.Background(), "anything"); !errors.Is(err, ErrClosed) {
		t.Fatalf("revoke after Close must fail, got %v", err)
	}
	if required, err := manager.authRequired(context.Background()); !errors.Is(err, ErrClosed) || !required {
		t.Fatalf("authRequired after Close must fail closed, required=%v err=%v", required, err)
	}
}

func TestConcurrentAuthorization(t *testing.T) {
	manager, _ := openTestManager(t, "")
	_, secret := mustCreateToken(t, manager, "reader", false)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if err := manager.Authorize(secret, "query"); err != nil {
					t.Errorf("concurrent authorized call failed: %v", err)
					return
				}
				if err := manager.Authorize("not-a-token", "query"); err == nil {
					t.Error("concurrent unauthorized call accepted")
					return
				}
				if j%5 == 0 {
					rec, _, err := manager.createToken(context.Background(), "worker", false)
					if err != nil {
						t.Errorf("concurrent create failed: %v", err)
						return
					}
					if _, err := manager.revokeToken(context.Background(), rec.ID); err != nil {
						t.Errorf("concurrent revoke failed: %v", err)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}

func TestTokenFromContext(t *testing.T) {
	if got := TokenFromContext(context.Background()); got != "" {
		t.Fatalf("empty context must yield empty token, got %q", got)
	}
	//nolint:staticcheck // nil contexts are treated like an unset value.
	if got := TokenFromContext(nil); got != "" {
		t.Fatalf("nil context must yield empty token, got %q", got)
	}
	ctx := withToken(context.Background(), "abc")
	if got := TokenFromContext(ctx); got != "abc" {
		t.Fatalf("TokenFromContext = %q, want %q", got, "abc")
	}
}
