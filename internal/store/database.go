package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Config selects one database. DSN is secret and must never be logged.
type Config struct {
	Driver string
	Path   string
	DSN    string
}

// OpenDatabase retains Open's SQLite file protections and selects pure-Go server
// drivers. Opening another engine does not transfer data between databases.
func OpenDatabase(ctx context.Context, config Config) (*Store, error) {
	if config.Driver == "" || config.Driver == "sqlite" {
		if config.DSN != "" {
			return nil, errors.New("SQLite uses a database path, not a DSN")
		}
		return Open(ctx, config.Path)
	}
	var db *sql.DB
	switch config.Driver {
	case "postgres":
		parsed, err := pgx.ParseConfig(config.DSN)
		if err != nil || config.DSN == "" {
			return nil, errors.New("invalid PostgreSQL database DSN")
		}
		parsed.ConnectTimeout = 5 * time.Second
		parsed.RuntimeParams["synchronous_commit"] = "on"
		parsed.RuntimeParams["lock_timeout"] = "5s"
		parsed.RuntimeParams["client_encoding"] = "UTF8"
		db = stdlib.OpenDB(*parsed)
	case "mariadb", "mysql":
		parsed, err := mysql.ParseDSN(config.DSN)
		if err != nil || parsed == nil || parsed.DBName == "" || parsed.User == "" {
			return nil, errors.New("invalid MariaDB/MySQL DSN; specify a user and database")
		}
		// These options are application invariants, not configurable escape hatches.
		parsed.MultiStatements = false
		parsed.InterpolateParams = false
		parsed.AllowAllFiles = false
		parsed.ClientFoundRows = true
		parsed.Timeout = 5 * time.Second
		if err := parsed.Apply(mysql.Charset("utf8mb4", "utf8mb4_bin")); err != nil {
			return nil, errors.New("configure database character set failed")
		}
		if parsed.Params == nil {
			parsed.Params = make(map[string]string)
		}
		parsed.Params["sql_mode"] = "'STRICT_ALL_TABLES,NO_ENGINE_SUBSTITUTION'"
		parsed.Params["autocommit"] = "1"
		parsed.Params["foreign_key_checks"] = "1"
		parsed.Params["unique_checks"] = "1"
		if config.Driver == "mariadb" {
			parsed.Params["check_constraint_checks"] = "1"
		}
		parsed.Params["innodb_lock_wait_timeout"] = "5"
		connector, err := mysql.NewConnector(parsed)
		if err != nil {
			return nil, errors.New("invalid MariaDB/MySQL connection settings")
		}
		db = sql.OpenDB(connector)
	default:
		return nil, errors.New("unsupported database driver")
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(5 * time.Minute)
	s := &Store{db: &database{DB: db, dialect: config.Driver}}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		// Driver errors can include passwords, connection strings, or server messages.
		return nil, errors.New("database connection failed; check PIPED_DATABASE_DSN and server availability")
	}
	if err := s.verifyDurability(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.migrateServer(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) verifyDurability(ctx context.Context) error {
	var enabled string
	query := "SHOW fsync"
	if s.db.dialect != "postgres" {
		query = "SELECT @@innodb_flush_log_at_trx_commit"
	}
	if err := s.db.DB.QueryRowContext(ctx, query).Scan(&enabled); err != nil {
		return errors.New("cannot verify database durability settings")
	}
	if enabled != "on" && enabled != "1" {
		return errors.New("database requires fsync=on (PostgreSQL) or innodb_flush_log_at_trx_commit=1 (MariaDB/MySQL)")
	}
	return nil
}

// database and transaction keep SQL binding in one place. Server transactions
// take a database row lock before reading authorization or mutable state. This
// preserves SQLite's immediate-transaction ordering across CLI/app connections,
// including first-user creation, revocation, versions, snapshots and scheduling.
// Direct writes join this order too. Reads outside transactions remain concurrent.
type database struct {
	*sql.DB
	dialect string
}

type transaction struct {
	*sql.Tx
	database *database
}

func (d *database) BeginTx(ctx context.Context, options *sql.TxOptions) (*transaction, error) {
	if d.dialect != "sqlite" {
		options = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	tx, err := d.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	if d.dialect != "sqlite" {
		var id int
		if err := tx.QueryRowContext(ctx, "SELECT id FROM store_lock WHERE id = 1 FOR UPDATE").Scan(&id); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	return &transaction{Tx: tx, database: d}, nil
}

func (d *database) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if d.dialect == "sqlite" {
		return d.DB.ExecContext(ctx, d.query(query), sqlArguments(args)...)
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

func (d *database) Exec(query string, args ...any) (sql.Result, error) {
	return d.ExecContext(context.Background(), query, args...)
}

func (d *database) QueryRow(query string, args ...any) *sql.Row {
	return d.QueryRowContext(context.Background(), query, args...)
}

func (d *database) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, d.query(query), sqlArguments(args)...)
}

func (d *database) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, d.query(query), sqlArguments(args)...)
}

func (t *transaction) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, t.database.query(query), sqlArguments(args)...)
}

func (t *transaction) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, t.database.query(query), sqlArguments(args)...)
}

func (t *transaction) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, t.database.query(query), sqlArguments(args)...)
}

// queryStrings reads one string column in query order and closes the result
// before returning, so callers can safely issue another transactional query.
func (t *transaction) queryStrings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := t.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, rows.Close()
}

// All schemas store booleans as constrained integers, keeping existing SQLite
// databases compatible and avoiding driver-specific boolean parameter inference.
func sqlArguments(args []any) []any {
	for i, argument := range args {
		if value, ok := argument.(bool); ok {
			args[i] = int64(0)
			if value {
				args[i] = int64(1)
			}
		}
	}
	return args
}

func (d *database) query(query string) string {
	// Queries are application-owned SQL, never user text. These identifiers
	// preserve SQLite's existing trigger column and implicit insertion sequence.
	if d.dialect == "sqlite" {
		query = strings.ReplaceAll(query, "run_source", "trigger")
		query = strings.ReplaceAll(query, "run_sequence", "rowid")
	}
	if d.dialect != "postgres" {
		return query
	}
	var result strings.Builder
	parameter := 0
	var quote byte
	for i := 0; i < len(query); i++ {
		character := query[i]
		if quote != 0 {
			result.WriteByte(character)
			if character == quote {
				if i+1 < len(query) && query[i+1] == quote {
					i++
					result.WriteByte(quote)
				} else {
					quote = 0
				}
			}
		} else if character == '\'' || character == '"' {
			quote = character
			result.WriteByte(character)
		} else if character == '?' {
			parameter++
			result.WriteByte('$')
			result.WriteString(strconv.Itoa(parameter))
		} else {
			result.WriteByte(character)
		}
	}
	return result.String()
}

func uniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	var mysqlErr *mysql.MySQLError
	var postgresErr *pgconn.PgError
	return (errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE) ||
		(errors.As(err, &mysqlErr) && mysqlErr.Number == 1062) ||
		(errors.As(err, &postgresErr) && postgresErr.Code == "23505")
}

func foreignKeyViolation(err error) bool {
	var sqliteErr *sqlite.Error
	var mysqlErr *mysql.MySQLError
	var postgresErr *pgconn.PgError
	// PostgreSQL RESTRICT reports restrict_violation (23001) on newer servers.
	// SQLite reports immediate ON DELETE RESTRICT failures as trigger errors.
	// Match their diagnostic as well so unrelated trigger failures stay errors.
	return (errors.As(err, &sqliteErr) && (sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY ||
		(sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_TRIGGER && strings.Contains(sqliteErr.Error(), "FOREIGN KEY constraint failed")))) ||
		(errors.As(err, &mysqlErr) && (mysqlErr.Number == 1451 || mysqlErr.Number == 1452)) ||
		(errors.As(err, &postgresErr) && (postgresErr.Code == "23503" || postgresErr.Code == "23001"))
}
