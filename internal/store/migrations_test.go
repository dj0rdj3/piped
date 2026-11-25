package store

import (
	"context"
	"database/sql"
	"strings"
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
	// Leave the final schema in place but remove its history record so its DDL
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

func TestMigrationFilesDefineOneTableEach(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres", "mysql"} {
		entries, err := migrations.ReadDir("migrations/" + dialect)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			body, err := migrations.ReadFile("migrations/" + dialect + "/" + entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			if strings.Count(text, "CREATE TABLE ") != 1 || strings.Contains(text, "ALTER TABLE ") || strings.Contains(text, "DROP TABLE ") {
				t.Errorf("%s/%s must define one final table without incremental DDL", dialect, entry.Name())
			}
		}
	}
}
