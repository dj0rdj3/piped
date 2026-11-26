package store

import (
	"database/sql"
	"strconv"
	"strings"
)

// Adapters describe the differences that affect safety and behavior. Schema files
// remain vendor-specific; application operations keep one authorization contract.
type databaseAdapter struct {
	open                             func(Config) (*sql.DB, error)
	bind                             func(string) string
	contains                         func(string) string
	writerLock                       bool
	durabilityQuery, durabilityValue string
	owner                            ownershipQueries
	maintenanceSettings              []string
	maintenanceCancel                string
	reclamation                      string
}

type ownershipQueries struct{ acquire, release, identity, held string }

var sqliteAdapter = databaseAdapter{bind: bindSQLite, contains: func(column string) string { return "instr(" + column + ", ?) > 0" }}
var postgresAdapter = databaseAdapter{
	open: openPostgres, bind: bindPostgres, writerLock: true,
	contains:        func(column string) string { return "strpos(" + column + ", ?) > 0" },
	durabilityQuery: "SHOW fsync", durabilityValue: "on",
	owner: ownershipQueries{
		acquire:  "SELECT pg_try_advisory_lock(7069706565)",
		release:  "SELECT pg_advisory_unlock(7069706565)",
		identity: "SELECT pg_backend_pid()",
		held: `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype = 'advisory'
        AND pid = ? AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
        AND classid::bigint * 4294967296 + objid::bigint = 7069706565)`,
	},
	maintenanceSettings: []string{`SET lock_timeout = '1ms'`, `SET statement_timeout = '1s'`},
	maintenanceCancel:   "SELECT pg_cancel_backend(", reclamation: "postgres",
}

var mysqlOwnership = ownershipQueries{
	acquire:  `SELECT GET_LOCK(SHA2(CONCAT(DATABASE(), '.piped.execution'), 256), 0)`,
	release:  `SELECT RELEASE_LOCK(SHA2(CONCAT(DATABASE(), '.piped.execution'), 256))`,
	identity: "SELECT CONNECTION_ID()",
	held:     `SELECT COALESCE(IS_USED_LOCK(SHA2(CONCAT(DATABASE(), '.piped.execution'), 256)), 0) = ?`,
}
var mysqlAdapter = databaseAdapter{
	open: openMySQL, bind: bindIdentity, writerLock: true,
	contains:        func(column string) string { return "LOCATE(?, " + column + ") > 0" },
	durabilityQuery: "SELECT @@innodb_flush_log_at_trx_commit", durabilityValue: "1",
	owner: mysqlOwnership,
}
var mariaDBAdapter = databaseAdapter{
	open: openMySQL, bind: bindIdentity, writerLock: true,
	contains:        func(column string) string { return "LOCATE(?, " + column + ") > 0" },
	durabilityQuery: "SELECT @@innodb_flush_log_at_trx_commit", durabilityValue: "1",
	owner:               mysqlOwnership,
	maintenanceSettings: []string{`SET SESSION lock_wait_timeout = 0, innodb_lock_wait_timeout = 1, max_statement_time = 1`},
	maintenanceCancel:   "KILL QUERY ", reclamation: "mariadb",
}

func adapterFor(driver string) *databaseAdapter {
	switch driver {
	case "sqlite":
		return &sqliteAdapter
	case "postgres":
		return &postgresAdapter
	case "mysql":
		return &mysqlAdapter
	case "mariadb":
		return &mariaDBAdapter
	default:
		return nil
	}
}

func (d *database) adapter() *databaseAdapter { return adapterFor(d.dialect) }
func bindIdentity(query string) string        { return query }
func bindSQLite(query string) string {
	query = strings.ReplaceAll(query, "run_source", "trigger")
	return strings.ReplaceAll(query, "run_sequence", "rowid")
}
func bindPostgres(query string) string {
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
