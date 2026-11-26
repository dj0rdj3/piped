package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOutputArchiveServerDiskUsage(t *testing.T) {
	container := os.Getenv("PIPED_TEST_DATABASE_CONTAINER")
	if container == "" {
		t.Skip("requires a disposable database container with disk-backed storage")
	}
	db, actor, job := scheduledJobFixture(t)
	if db.db.dialect != "postgres" && db.db.dialect != "mariadb" {
		t.Skip("PostgreSQL/MariaDB disk accounting")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var directory, settingsValue string
	settings := make(map[string]string)
	if db.db.dialect == "postgres" {
		var dataDirectory, oid string
		if err := db.db.QueryRowContext(ctx, `SELECT current_setting('data_directory'), oid::text FROM pg_database WHERE datname = current_database()`).Scan(&dataDirectory, &oid); err != nil {
			t.Fatal(err)
		}
		directory = filepath.Join(dataDirectory, "base", oid)
		if err := db.db.QueryRowContext(ctx, `SHOW default_toast_compression`).Scan(&settingsValue); err != nil {
			t.Fatal(err)
		}
		settings["default_toast_compression"] = settingsValue
		// Keep automatic vacuum from racing these explicitly separated stages.
		for _, table := range []string{"run_output", "run_output_archives"} {
			if _, err := db.db.ExecContext(ctx, `ALTER TABLE `+table+` SET (autovacuum_enabled = false)`); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		var dataDirectory, name string
		if err := db.db.QueryRowContext(ctx, `SELECT @@datadir, DATABASE(), @@innodb_file_per_table`).Scan(&dataDirectory, &name, &settingsValue); err != nil {
			t.Fatal(err)
		}
		settings["innodb_file_per_table"] = settingsValue
		directory = filepath.Join(dataDirectory, name)
	}
	drain := func(query string) {
		t.Helper()
		rows, err := db.db.QueryContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
		}
		readErr := rows.Err()
		closeErr := rows.Close()
		if readErr != nil || closeErr != nil {
			t.Fatal("maintenance result", readErr, closeErr)
		}
	}
	type space struct {
		PayloadBytes   int64 `json:"live_log_payload_bytes"`
		RelationBytes  int64 `json:"log_relation_bytes"`
		DirectoryBytes int64 `json:"database_directory_allocated_bytes"`
		ReusableBytes  int64 `json:"reported_reusable_bytes,omitempty"`
	}
	measure := func() space {
		t.Helper()
		var result space
		if err := db.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT SUM(LENGTH(data)) FROM run_output), 0) + COALESCE((SELECT SUM(LENGTH(data)) FROM run_output_archives), 0)`).Scan(&result.PayloadBytes); err != nil {
			t.Fatal(err)
		}
		if db.db.dialect == "postgres" {
			if err := db.db.QueryRowContext(ctx, `SELECT pg_total_relation_size('run_output') + pg_total_relation_size('run_output_archives')`).Scan(&result.RelationBytes); err != nil {
				t.Fatal(err)
			}
		} else {
			drain(`ANALYZE TABLE run_output, run_output_archives`)
			if err := db.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(FILE_SIZE), 0), COALESCE(SUM(ALLOCATED_SIZE), 0) FROM information_schema.INNODB_SYS_TABLESPACES WHERE NAME IN (CONCAT(DATABASE(), '/run_output'), CONCAT(DATABASE(), '/run_output_archives'))`).Scan(&result.RelationBytes, &result.ReusableBytes); err != nil {
				t.Fatal(err)
			}
			// DATA_FREE reports reusable space; ALLOCATED_SIZE is allocation, not free bytes.
			if err := db.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(DATA_FREE), 0) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ('run_output', 'run_output_archives')`).Scan(&result.ReusableBytes); err != nil {
				t.Fatal(err)
			}
		}
		output, err := exec.CommandContext(ctx, "docker", "exec", container, "du", "-s", "-B1", directory).Output()
		if err != nil {
			t.Fatal("measure disposable database directory allocation", err)
		}
		fields := strings.Fields(string(output))
		if len(fields) < 1 {
			t.Fatal("missing filesystem allocation")
		}
		result.DirectoryBytes, err = strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	stages := map[string]space{"baseline": measure()}
	if _, err := db.AdmitRun(ctx, actor, RunAdmission{ID: "archive", RequestID: "archive", JobID: job.ID, Version: job.Version}); err != nil {
		t.Fatal(err)
	}
	const size = 8 * 1024 * 1024
	data := make([]byte, 0, size+256)
	for line := 0; len(data) < size; line++ {
		data = fmt.Appendf(data, "2026-11-25T12:%02d:%02dZ INFO host=fixture sequence=%08d result=ok\n", line/60%60, line%60, line)
	}
	data = data[:size]
	for offset := 0; offset < len(data); offset += MaxOutputBatchBytes {
		if err := db.AppendOutput(ctx, "archive", "host", data[offset:min(offset+MaxOutputBatchBytes, len(data))]); err != nil {
			t.Fatal(err)
		}
	}
	stages["raw"] = measure()
	if err := db.SetRunHost(ctx, "archive", "host", "succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.CompressRunOutput(ctx, "archive", "host"); err != nil {
		t.Fatal(err)
	}
	stages["compressed_before_cleanup"] = measure()
	if stages["compressed_before_cleanup"].PayloadBytes >= size/10 {
		t.Fatal("fixture did not compress")
	}
	if err := db.FinishRun(ctx, "archive", "succeeded"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitRun(ctx, actor, RunAdmission{ID: "active", RequestID: "active", JobID: job.ID, Version: job.Version}); err != nil {
		t.Fatal(err)
	}
	beforeBusy := measure()
	if err := db.ReclaimServerOutput(ctx, ctx); err != nil {
		t.Fatal(err)
	}
	stages["deferred_for_active_job"] = measure()
	if stages["deferred_for_active_job"].RelationBytes != beforeBusy.RelationBytes {
		t.Fatal("reclamation rebuilt tables while a job was active")
	}
	if err := db.FinishRun(ctx, "active", "succeeded"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := db.ReclaimServerOutput(ctx, ctx); err != nil {
		t.Fatal(err)
	}
	stages["automatic_idle_reclamation"] = measure()
	if stages["automatic_idle_reclamation"].DirectoryBytes >= stages["raw"].DirectoryBytes || stages["automatic_idle_reclamation"].RelationBytes >= stages["raw"].RelationBytes {
		t.Fatal("compression did not reduce physical database/relation allocation after reclaim")
	}
	hash := sha256.New()
	for _, chunk := range readAllOutput(t, db, actor) {
		hash.Write(chunk.Data)
	}
	want := sha256.Sum256(data)
	if !bytes.Equal(hash.Sum(nil), want[:]) {
		t.Fatal("cleanup changed archived bytes or cursors")
	}
	report := map[string]any{"driver": db.db.dialect, "settings": settings, "stages": stages, "byte_exact_replay": true, "maintenance_scope": "disposable fixture only; WAL/redo/binlogs excluded from per-database directory"}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Server disk measurements: %s", encoded)
}
