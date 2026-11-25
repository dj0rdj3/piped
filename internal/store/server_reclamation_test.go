package store

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"
)

func TestServerReclamationInterruptWaitsForServer(t *testing.T) {
	db := openTest(t)
	if !db.SupportsServerReclamation() {
		t.Skip("requires PostgreSQL or MariaDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := db.db.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer conn.Raw(func(any) error { return driver.ErrBadConn })
	for _, phase := range []string{"running query", "waiting DDL"} {
		t.Run(phase, func(t *testing.T) {
			query := `SELECT pg_sleep(4)`
			if db.db.dialect == "mariadb" {
				query = `SELECT SLEEP(4)`
			}
			if phase == "waiting DDL" {
				if _, err := db.db.DB.ExecContext(ctx, `CREATE TABLE reclamation_cancel_fixture (id INTEGER PRIMARY KEY)`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.db.DB.ExecContext(ctx, `INSERT INTO reclamation_cancel_fixture VALUES (7)`); err != nil {
					t.Fatal(err)
				}
				blocker, err := db.db.DB.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback()
				var id int
				if err := blocker.QueryRowContext(ctx, `SELECT id FROM reclamation_cancel_fixture`).Scan(&id); err != nil {
					t.Fatal(err)
				}
				query = `ALTER TABLE reclamation_cancel_fixture ADD COLUMN cancelled_column INTEGER`
			}
			interrupt, request := context.WithCancel(ctx)
			defer request()
			stop, err := db.interruptServerReclamation(ctx, interrupt, conn)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			done := make(chan error, 1)
			go func() {
				_, err := conn.ExecContext(ctx, query)
				done <- err
			}()
			select {
			case err := <-done:
				t.Fatalf("fixture did not block: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			started := time.Now()
			request()
			select {
			case err := <-done:
				// MariaDB SLEEP returns 1 for interruption rather than an error.
				if err == nil && (phase == "waiting DDL" || db.db.dialect == "postgres") {
					t.Fatal("statement completed instead of being interrupted")
				}
			case <-time.After(time.Second):
				t.Fatal("server did not acknowledge interruption promptly")
			}
			t.Logf("server acknowledged %s cancellation in %s", phase, time.Since(started))
		})
	}
	var id int
	if err := conn.QueryRowContext(ctx, `SELECT id FROM reclamation_cancel_fixture`).Scan(&id); err != nil || id != 7 {
		t.Fatal("cancelled DDL changed existing data", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT cancelled_column FROM reclamation_cancel_fixture`); err == nil {
		t.Fatal("cancelled DDL changed the schema")
	}
}
