package store

import (
	"database/sql"
	"errors"
	"strconv"
	"time"

	"piped/internal/lifecycle"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func openPostgres(config Config) (*sql.DB, error) {
	parsed, err := pgx.ParseConfig(config.DSN)
	if err != nil || config.DSN == "" {
		return nil, errors.New("invalid PostgreSQL database DSN")
	}
	parsed.ConnectTimeout = lifecycle.StorageTimeout
	parsed.RuntimeParams["synchronous_commit"] = "on"
	parsed.RuntimeParams["lock_timeout"] = lifecycle.StorageTimeout.String()
	parsed.RuntimeParams["client_encoding"] = "UTF8"
	return stdlib.OpenDB(*parsed), nil
}

func openMySQL(config Config) (*sql.DB, error) {
	parsed, err := mysql.ParseDSN(config.DSN)
	if err != nil || parsed == nil || parsed.DBName == "" || parsed.User == "" {
		return nil, errors.New("invalid MariaDB/MySQL DSN; specify a user and database")
	}
	// These options are application invariants, not configurable escape hatches.
	parsed.MultiStatements = false
	parsed.InterpolateParams = false
	parsed.AllowAllFiles = false
	parsed.ClientFoundRows = true
	parsed.Timeout = lifecycle.StorageTimeout
	// Bound established network I/O as well as dialing, including COMMIT
	// and ROLLBACK, whose driver methods have no context argument.
	if parsed.ReadTimeout <= 0 || parsed.ReadTimeout > lifecycle.StorageTimeout {
		parsed.ReadTimeout = lifecycle.StorageTimeout
	}
	if parsed.WriteTimeout <= 0 || parsed.WriteTimeout > lifecycle.StorageTimeout {
		parsed.WriteTimeout = lifecycle.StorageTimeout
	}
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
	parsed.Params["innodb_lock_wait_timeout"] = strconv.Itoa(int(lifecycle.StorageTimeout / time.Second))
	connector, err := mysql.NewConnector(parsed)
	if err != nil {
		return nil, errors.New("invalid MariaDB/MySQL connection settings")
	}
	return sql.OpenDB(connector), nil
}
