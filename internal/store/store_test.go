package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"piped/internal/testdatabase"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	driver, dsn := testdatabase.Settings(t)
	s, err := OpenDatabase(context.Background(), Config{Driver: driver, DSN: dsn, Path: filepath.Join(t.TempDir(), "data", "piped.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func actorFor(t *testing.T, s *Store, u User, token string) Actor {
	t.Helper()
	if err := s.CreateSession(context.Background(), u, token, time.Now().Unix(), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	return Actor{User: u, SessionHash: token}
}

func TestMigrationsAndPersistence(t *testing.T) {
	ctx := context.Background()
	filename := filepath.Join(t.TempDir(), "data", "piped.db")
	s, err := Open(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	migrationFiles, err := migrations.ReadDir("migrations/sqlite")
	if err != nil {
		t.Fatal(err)
	}
	var migrationCount, latestVersion int
	if err := s.db.QueryRow("SELECT COUNT(*), MAX(version) FROM schema_migrations").Scan(&migrationCount, &latestVersion); err != nil {
		t.Fatal(err)
	}
	if migrationCount != len(migrationFiles) || latestVersion != len(migrationFiles) {
		t.Fatalf("fresh database did not apply all schema files: count=%d version=%d files=%d", migrationCount, latestVersion, len(migrationFiles))
	}
	u, err := s.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actorFor(t, s, u, "persistent-session-hash")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(filename + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("insecure database permissions: %s", info.Mode())
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.UserByName(ctx, "ADMIN")
	if err != nil || got.ID != u.ID {
		t.Fatalf("persisted user: %+v %v", got, err)
	}
	if _, err := s.Session(ctx, "persistent-session-hash", time.Now().Unix()); err != nil {
		t.Fatalf("session did not survive database reopen: %v", err)
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode: %s %v", mode, err)
	}
	if _, err := s.db.Exec("UPDATE schema_migrations SET checksum = 'tampered'"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err == nil {
		t.Fatal("accepted modified migration")
	}
}

func TestNewerDatabaseRejected(t *testing.T) {
	s := openTest(t)
	if _, err := s.db.Exec("INSERT INTO schema_migrations(version,checksum) VALUES(999,'future')"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(context.Background()); err == nil {
		t.Fatal("accepted future schema")
	}
}

func TestPrivateDatabasePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), filepath.Join(dir, "piped.db")); err == nil {
		t.Fatal("accepted public directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "piped.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), link); err == nil {
		t.Fatal("accepted database symlink")
	}
}

func TestConcurrentBootstrap(t *testing.T) {
	s := openTest(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.InitAdmin(context.Background(), "admin", "hash")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrInitialized) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("created %d initial administrators", successes)
	}
}

func TestSessionExpiryAndPasswordInvalidation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u, err := s.InitAdmin(ctx, "admin", "old-hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, s, u, "session-hash")
	now := time.Now().Unix()
	if err := s.CreateSession(ctx, u, "expired", now-10, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, "expired", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session: %v", err)
	}
	if err := s.PurgeSessions(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, actor, u.ID, "new-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, actor.SessionHash, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survived password change: %v", err)
	}
	if err := s.CreateSession(ctx, u, "stale-login", now, now+100); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale password created session: %v", err)
	}
	if _, err := s.CreateUser(ctx, actor, "other", "hash"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale actor changed users: %v", err)
	}
}

func TestUsersConstraintsAndCascade(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u, err := s.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	a := actorFor(t, s, u, "admin-session")
	if err := s.DeleteUser(ctx, a, u.ID); !errors.Is(err, ErrLastUser) {
		t.Fatalf("removed last user: %v", err)
	}
	other, err := s.CreateUser(ctx, a, "member", "hash")
	if err != nil {
		t.Fatal(err)
	}
	users, err := s.Users(ctx)
	if err != nil || len(users) != 2 {
		t.Fatalf("user metadata listing failed: %v", err)
	}
	for _, user := range users {
		if user.PasswordHash != "" || user.ID == 0 || user.Username == "" || user.CreatedAt == 0 {
			t.Fatal("user metadata loaded a password hash or lost public fields")
		}
	}
	if _, err := s.CreateUser(ctx, a, "MEMBER", "hash"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate username: %v", err)
	}
	actorFor(t, s, other, "other-session")
	if err := s.DeleteUser(ctx, a, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, "other-session", time.Now().Unix()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted user's session survived: %v", err)
	}
	if err := s.DeleteUser(ctx, a, 999); !errors.Is(err, ErrLastUser) {
		t.Fatalf("last user guard: %v", err)
	}
	if _, err := s.UserByName(ctx, "' OR 1=1--"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("query injection: %v", err)
	}
	if err := s.ChangePassword(ctx, a, 999, "hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("updated missing user: %v", err)
	}
}
