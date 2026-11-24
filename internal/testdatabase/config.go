// Package testdatabase creates isolated server databases for opt-in integration
// checks. It is imported only by tests and never touches the deployment database.
package testdatabase

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Settings returns SQLite defaults unless the Docker harness supplies a private
// JSON file. Secrets travel through the file, never test output or process argv.
func Settings(t testing.TB) (string, string) {
	t.Helper()
	path := os.Getenv("PIPED_TEST_DATABASE_CONFIG")
	if path == "" {
		return "sqlite", ""
	}
	var config struct{ Driver, DSN string }
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("integration settings must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read integration settings failed")
	}
	defer clear(data)
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal("invalid integration settings")
	}
	random := make([]byte, 12)
	_, _ = rand.Read(random)
	name := "piped_test_" + hex.EncodeToString(random)
	var admin *sql.DB
	var dsn, quote string
	switch config.Driver {
	case "postgres":
		parsed, err := pgx.ParseConfig(config.DSN)
		if err != nil {
			t.Fatal("invalid integration DSN")
		}
		admin = stdlib.OpenDB(*parsed)
		dsn = parsed.ConnString()
		// ConnString retains the original DSN, so serialize the database override.
		if parsedURL, err := url.Parse(dsn); err == nil && (parsedURL.Scheme == "postgres" || parsedURL.Scheme == "postgresql") {
			parsedURL.Path = "/" + name
			parsedURL.RawPath = ""
			query := parsedURL.Query()
			query.Del("database")
			query.Del("dbname")
			parsedURL.RawQuery = query.Encode()
			dsn = parsedURL.String()
		} else {
			dsn += " dbname=" + name
		}
		quote = `"`
	case "mysql", "mariadb":
		parsed, err := mysql.ParseDSN(config.DSN)
		if err != nil {
			t.Fatal("invalid integration DSN")
		}
		connector, err := mysql.NewConnector(parsed)
		if err != nil {
			t.Fatal("invalid integration connector")
		}
		admin = sql.OpenDB(connector)
		parsed.DBName = name
		dsn = parsed.FormatDSN()
		quote = "`"
	default:
		t.Fatal("unsupported integration database")
	}
	if _, err := admin.ExecContext(context.Background(), "CREATE DATABASE "+quote+name+quote); err != nil {
		admin.Close()
		t.Fatal("create isolated test database failed")
	}
	t.Cleanup(func() {
		defer admin.Close()
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE "+quote+name+quote); err != nil {
			t.Error("drop isolated test database failed")
		}
	})
	return config.Driver, dsn
}
