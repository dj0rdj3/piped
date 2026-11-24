package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationHistoryRejectsCorruptionAcrossDrivers(t *testing.T) {
	for name, statement := range map[string]string{
		"gap":      "DELETE FROM schema_migrations WHERE version = 2",
		"zero":     "INSERT INTO schema_migrations(version, checksum) VALUES (0, 'invalid')",
		"negative": "INSERT INTO schema_migrations(version, checksum) VALUES (-1, 'invalid')",
		"future":   "INSERT INTO schema_migrations(version, checksum) VALUES (999, 'future')",
		"pending":  "UPDATE schema_migrations SET checksum = 'pending' WHERE version = 1",
		"tampered": "UPDATE schema_migrations SET checksum = 'tampered' WHERE version = 1",
	} {
		t.Run(name, func(t *testing.T) {
			db := openTest(t)
			ctx := context.Background()
			user, err := db.InitAdmin(ctx, "admin", "unchanged-hash")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.db.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
			if err := db.migrate(ctx); err == nil {
				t.Fatal("corrupt history was accepted")
			}
			stored, err := db.UserByName(ctx, user.Username)
			if err != nil || stored != user {
				t.Fatalf("rejected migration changed existing account data: %v", err)
			}
		})
	}
}

func TestFailedMigrationPreservesTransactionalOrPendingHistory(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	var latest int
	if err := db.db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&latest); err != nil {
		t.Fatal(err)
	}
	// Leave the final table in place but remove its history record so its DDL
	// fails. Only nontransactional MySQL/MariaDB DDL may leave a pending marker.
	if _, err := db.db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = ?", latest); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.migrate(ctx); err == nil {
			t.Fatal("partially applied schema was accepted")
		}
		var checksum string
		err := db.db.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version = ?", latest).Scan(&checksum)
		if db.db.dialect == "mysql" || db.db.dialect == "mariadb" {
			if err != nil || checksum != "pending" {
				t.Fatalf("interrupted DDL was not marked pending: %q %v", checksum, err)
			}
		} else if err != sql.ErrNoRows {
			t.Fatalf("failed transactional migration committed history: %q %v", checksum, err)
		}
	}
}

func TestSQLiteUpgradesPreserveExistingAccountsAndSessions(t *testing.T) {
	for _, applied := range []int{2, 5, 9, 12} {
		t.Run(fmt.Sprintf("from-version-%d", applied), func(t *testing.T) {
			ctx := context.Background()
			directory := filepath.Join(t.TempDir(), "data")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(directory, "piped.db")
			raw, err := sql.Open("sqlite", filename)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { raw.Close() })
			if _, err := raw.ExecContext(ctx, "CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, checksum TEXT NOT NULL)"); err != nil {
				t.Fatal(err)
			}
			entries, err := migrations.ReadDir("migrations/sqlite")
			if err != nil {
				t.Fatal(err)
			}
			for index, entry := range entries[:applied] {
				body, err := migrations.ReadFile("migrations/sqlite/" + entry.Name())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := raw.ExecContext(ctx, string(body)); err != nil {
					t.Fatal(err)
				}
				if _, err := raw.ExecContext(ctx, "INSERT INTO schema_migrations(version, checksum) VALUES (?, ?)", index+1, fmt.Sprintf("%x", sha256.Sum256(body))); err != nil {
					t.Fatal(err)
				}
			}
			legacy := &Store{db: &database{DB: raw, dialect: "sqlite"}}
			user, err := legacy.InitAdmin(ctx, "admin", "existing-password-hash")
			if err != nil {
				t.Fatal(err)
			}
			actor := actorFor(t, legacy, user, "existing-session")
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			upgraded, err := Open(ctx, filename)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { upgraded.Close() })
			resolved, err := upgraded.Session(ctx, actor.SessionHash, user.CreatedAt)
			if err != nil || resolved.User != user {
				t.Fatalf("upgrade lost the account, hash, or session: %v", err)
			}
			var count int
			if err := upgraded.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil || count != len(entries) {
				t.Fatalf("upgrade did not apply the remaining migrations: %v", err)
			}
			if err := upgraded.migrate(ctx); err != nil {
				t.Fatalf("upgraded database failed a repeat migration check: %v", err)
			}
		})
	}
}
