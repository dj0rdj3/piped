package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-sql-driver/mysql"
	"piped/internal/testdatabase"
)

func TestCaseInsensitiveDatabaseAliasesShareExecutionOwnership(t *testing.T) {
	driver, dsn := testdatabase.Settings(t)
	if driver != "mysql" && driver != "mariadb" {
		t.Skip("MySQL/MariaDB database-name case folding")
	}
	ctx := context.Background()
	first, err := OpenDatabase(ctx, Config{Driver: driver, DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	var mode int
	if err := first.db.QueryRowContext(ctx, `SELECT @@lower_case_table_names`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode == 0 {
		t.Skip("requires a disposable server initialized with lower_case_table_names=1")
	}
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("parse disposable database alias")
	}
	parsed.DBName = strings.ToUpper(parsed.DBName)
	second, err := OpenDatabase(ctx, Config{Driver: driver, DSN: parsed.FormatDSN()})
	if err != nil {
		t.Fatal("open alternate spelling of the same disposable database", err)
	}
	defer second.Close()
	owner, err := first.AcquireExecutionOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	duplicate, err := second.AcquireExecutionOwner(ctx)
	if duplicate != nil {
		duplicate.Close()
	}
	if !errors.Is(err, ErrExecutionOwned) {
		t.Fatal("case alias allowed a second controller for the same database", err)
	}
}

func TestExecutionOwnerExcludesIndependentServersAndAllowsCLI(t *testing.T) {
	ctx := context.Background()
	driver, dsn := testdatabase.Settings(t)
	configuration := Config{Driver: driver, DSN: dsn, Path: filepath.Join(t.TempDir(), "data", "owner.db")}
	first, err := OpenDatabase(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenDatabase(ctx, configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	owner, err := first.AcquireExecutionOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	for _, db := range []*Store{first, second} {
		if duplicate, err := db.AcquireExecutionOwner(ctx); !errors.Is(err, ErrExecutionOwned) {
			if duplicate != nil {
				duplicate.Close()
			}
			t.Fatalf("second engine acquired ownership: %v", err)
		}
	}
	// CLI setup/key access uses its own pool and does not block execution.
	if _, err := second.InitAdmin(ctx, "admin", "hash"); err != nil {
		t.Fatal("ownership prevented ordinary database access", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Check(ctx); err != nil {
		t.Fatal("CLI close disturbed ownership", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := first.AcquireExecutionOwner(ctx)
	if err != nil {
		t.Fatal("ownership was not released", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentExecutionOwnerAcquisitionHasOneWinner(t *testing.T) {
	db := openTest(t)
	var group sync.WaitGroup
	owners := make(chan *ExecutionOwner, 4)
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			owner, err := db.AcquireExecutionOwner(context.Background())
			if err != nil && !errors.Is(err, ErrExecutionOwned) {
				t.Error(err)
			}
			if owner != nil {
				owners <- owner
			}
		}()
	}
	group.Wait()
	close(owners)
	count := 0
	for owner := range owners {
		count++
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	}
	if count != 1 {
		t.Fatalf("acquired %d owners", count)
	}
}

func TestExecutionOwnerDetectsLostServerConnection(t *testing.T) {
	db := openTest(t)
	if db.db.dialect == "sqlite" {
		t.Skip("server session ownership")
	}
	owner, err := db.AcquireExecutionOwner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Check(context.Background()); err == nil {
		t.Fatal("closed ownership session passed health check")
	}
	replacement, err := db.AcquireExecutionOwner(context.Background())
	if err != nil {
		t.Fatal("discarded connection retained its lock", err)
	}
	replacement.Close()
}
