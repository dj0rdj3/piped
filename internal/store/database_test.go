package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"piped/internal/testdatabase"

	"github.com/go-sql-driver/mysql"
)

func TestInvalidDatabaseSettingsDoNotExposeDSN(t *testing.T) {
	for _, config := range []Config{
		{Driver: "other", DSN: "secret"},
		{Driver: "sqlite", DSN: "secret"},
		{Driver: "postgres", DSN: "postgres://user:secret@host:invalid/database"},
		{Driver: "mysql", DSN: "secret"},
		{Driver: "mariadb", DSN: "user:secret@tcp(localhost)/"},
	} {
		_, err := OpenDatabase(context.Background(), config)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("invalid database configuration was accepted or leaked")
		}
	}
}

func TestDatabaseConnectionErrorsDoNotExposeCredentials(t *testing.T) {
	for _, config := range []Config{
		{Driver: "postgres", DSN: "postgres://fixture:private-connection-secret@127.0.0.1:1/piped?sslmode=disable"},
		{Driver: "mysql", DSN: "fixture:private-connection-secret@tcp(127.0.0.1:1)/piped"},
		{Driver: "mariadb", DSN: "fixture:private-connection-secret@tcp(127.0.0.1:1)/piped"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		db, err := OpenDatabase(ctx, config)
		cancel()
		if db != nil {
			db.Close()
		}
		if err == nil || strings.Contains(err.Error(), "private-connection-secret") || strings.Contains(err.Error(), "fixture") {
			t.Fatal("connection failure accepted or exposed credentials")
		}
	}
}

func TestIndependentConnectionsSerializeBootstrapAndRevocation(t *testing.T) {
	driver, dsn := testdatabase.Settings(t)
	if driver == "sqlite" {
		// SQLite locking is covered with independent file connections separately.
		t.Skip("server connection locking")
	}
	// Independent pools model the CLI and running application.
	ctx := context.Background()
	configuration := Config{Driver: driver, DSN: dsn}
	first, err := OpenDatabase(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	second, err := OpenDatabase(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	var group sync.WaitGroup
	results := make(chan error, 12)
	for index := range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			s := first
			if index%2 == 0 {
				s = second
			}
			_, err := s.InitAdmin(ctx, "admin", "hash")
			results <- err
		}()
	}
	group.Wait()
	close(results)
	created := 0
	for err := range results {
		if err == nil {
			created++
		} else if !errors.Is(err, ErrInitialized) {
			t.Fatal(err)
		}
	}
	if created != 1 {
		t.Fatalf("created %d initial users", created)
	}
	user, err := first.UserByName(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, first, user, "session")
	tx, err := first.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", actor.SessionHash); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := second.CreateUser(ctx, actor, "revoked-write", "hash")
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("authorization passed an uncommitted revocation: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked actor wrote after waiting for the lock: %v", err)
	}
}

func TestServerMigrationHistoryAndReopen(t *testing.T) {
	s := openTest(t)
	if s.db.dialect == "sqlite" {
		t.Skip("server migration lifecycle")
	}
	ctx := context.Background()
	if err := s.migrateServer(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE schema_migrations SET checksum = 'pending' WHERE version = 1"); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateServer(ctx); err == nil {
		t.Fatal("accepted incomplete or tampered schema")
	}
}

func TestUnicodeAndASCIINameUniqueness(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	user, err := s.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, s, user, "session")
	for index, name := range []string{"Caf\u00e9 \U0001f680", "CAF\u00e9 \U0001f680", "Cafe \U0001f680", "caf\u00c9 \U0001f680"} {
		_, err := s.CreateCredential(ctx, actor, Credential{ID: name, Name: name, PublicKey: "key", Fingerprint: "fingerprint", Algorithm: "ssh-ed25519"}, []byte("ciphertext"))
		if index == 1 {
			if !errors.Is(err, ErrCredentialName) {
				t.Fatalf("ASCII case duplicate: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.Credentials(ctx, actor)
	if err != nil || len(items) != 3 {
		t.Fatalf("Unicode metadata lost: %v", err)
	}
}

func TestDatabasePreservesMaximumSizedUnicodeFieldsAndScripts(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	user, err := s.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, s, user, "session")
	host := Host{
		ID: "host", Name: strings.Repeat("\U0001f680", 100),
		Address: strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61), Port: 22,
		Username: strings.Repeat("u", 128), Tags: []string{},
	}
	saved, err := s.SaveHost(ctx, actor, host, []byte("ciphertext"), nil, true)
	if err != nil || saved.Name != host.Name || saved.Username != host.Username || saved.Address != host.Address {
		t.Fatalf("maximum-sized host fields did not round trip: %v", err)
	}
	script := strings.Repeat("\u00e9", 32*1024)
	job, err := s.SaveJob(ctx, actor, Job{
		ID: "job", Name: strings.Repeat("\U0001f680", 100), Script: script,
		HostIDs: []string{host.ID}, ExecutionMode: "sequential",
	}, nil, true)
	if err != nil || job.Script != script {
		t.Fatalf("64 KiB Unicode script was lost: %v", err)
	}
	items, err := s.Jobs(ctx, actor)
	if err != nil || len(items) != 1 || items[0].Script != script {
		t.Fatalf("maximum script did not survive retrieval: %v", err)
	}
}

func TestServerConnectionOptionsPreserveTransactionAndConstraintChecks(t *testing.T) {
	driver, dsn := testdatabase.Settings(t)
	if driver != "mariadb" && driver != "mysql" {
		t.Skip("MariaDB/MySQL connection settings")
	}
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("parse fixture DSN failed")
	}
	if parsed.Params == nil {
		parsed.Params = make(map[string]string)
	}
	for _, name := range []string{"autocommit", "foreign_key_checks", "unique_checks"} {
		parsed.Params[name] = "0"
	}
	if driver == "mariadb" {
		parsed.Params["check_constraint_checks"] = "0"
	}
	db, err := OpenDatabase(context.Background(), Config{Driver: driver, DSN: parsed.FormatDSN()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Replacement connections must retain the enforced settings too.
	db.db.SetMaxIdleConns(0)
	for range 2 {
		var autocommit, foreignKeys, unique int
		err := db.db.QueryRow("SELECT @@autocommit, @@foreign_key_checks, @@unique_checks").Scan(&autocommit, &foreignKeys, &unique)
		if err != nil || autocommit != 1 || foreignKeys != 1 || unique != 1 {
			t.Fatalf("connection options weakened transactions or constraints: %v", err)
		}
		if driver == "mariadb" {
			var enabled int
			if err := db.db.QueryRow("SELECT @@check_constraint_checks").Scan(&enabled); err != nil || enabled != 1 {
				t.Fatalf("CHECK constraints were disabled: %v", err)
			}
		}
	}
}

func TestSQLiteForeignKeyRestrictionClassification(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "data", "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE parent (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE child (parent_id INTEGER REFERENCES parent(id) ON DELETE RESTRICT)`,
		`INSERT INTO parent(id) VALUES (1), (2)`,
		`INSERT INTO child(parent_id) VALUES (1)`,
		`CREATE TRIGGER deny_delete BEFORE DELETE ON parent WHEN OLD.id = 2 BEGIN SELECT RAISE(ABORT, 'unrelated trigger failure'); END`,
	} {
		if _, err := db.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, fixture := range []struct {
		statement string
		want      bool
	}{
		{`DELETE FROM parent WHERE id = 1`, true},
		{`INSERT INTO child(parent_id) VALUES (3)`, true},
		{`DELETE FROM parent WHERE id = 2`, false},
	} {
		_, err := db.db.Exec(fixture.statement)
		if err == nil || foreignKeyViolation(err) != fixture.want || foreignKeyViolation(fmt.Errorf("wrapped: %w", err)) != fixture.want {
			t.Fatalf("wrong classification for %s: %v", fixture.statement, err)
		}
	}
	if foreignKeyViolation(nil) || foreignKeyViolation(errors.New("FOREIGN KEY constraint failed")) {
		t.Fatal("classified an unrelated error as a foreign-key violation")
	}
}
