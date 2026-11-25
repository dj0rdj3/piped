package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"syscall"
	"testing"
)

type sqliteOutputSpace struct {
	PageSize                 int64 `json:"page_size"`
	Pages                    int64 `json:"pages"`
	FreePages                int64 `json:"free_pages"`
	UsedBytes                int64 `json:"used_bytes"`
	DatabaseBytes            int64 `json:"database_bytes"`
	DatabaseAllocatedBytes   int64 `json:"database_allocated_bytes"`
	WALBytesBeforeCheckpoint int64 `json:"wal_bytes_before_checkpoint"`
	WALBytesAfterCheckpoint  int64 `json:"wal_bytes_after_checkpoint"`
	WALAllocatedBefore       int64 `json:"wal_allocated_bytes_before_checkpoint"`
	WALAllocatedAfter        int64 `json:"wal_allocated_bytes_after_checkpoint"`
}

// Checkpoint only the isolated fixture so file size and page accounting describe
// the same committed data. Production databases are never vacuumed by this test.
func measureSQLiteOutputSpace(t *testing.T, db *Store) sqliteOutputSpace {
	t.Helper()
	ctx := context.Background()
	var sequence int
	var name, filename string
	if err := db.db.QueryRowContext(ctx, `PRAGMA database_list`).Scan(&sequence, &name, &filename); err != nil {
		t.Fatal(err)
	}
	fileSpace := func(path string) (int64, int64) {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			return 0, 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return info.Size(), info.Sys().(*syscall.Stat_t).Blocks * 512
	}
	var measurement sqliteOutputSpace
	measurement.WALBytesBeforeCheckpoint, measurement.WALAllocatedBefore = fileSpace(filename + "-wal")
	var busy, logFrames, checkpointed int
	if err := db.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil || busy != 0 {
		t.Fatalf("fixture checkpoint: busy=%d err=%v", busy, err)
	}
	for query, target := range map[string]*int64{
		`PRAGMA page_size`:      &measurement.PageSize,
		`PRAGMA page_count`:     &measurement.Pages,
		`PRAGMA freelist_count`: &measurement.FreePages,
	} {
		if err := db.db.QueryRowContext(ctx, query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	measurement.UsedBytes = (measurement.Pages - measurement.FreePages) * measurement.PageSize
	measurement.DatabaseBytes, measurement.DatabaseAllocatedBytes = fileSpace(filename)
	measurement.WALBytesAfterCheckpoint, measurement.WALAllocatedAfter = fileSpace(filename + "-wal")
	if measurement.DatabaseBytes != measurement.Pages*measurement.PageSize || measurement.WALBytesAfterCheckpoint != 0 {
		t.Fatal("checkpoint did not reconcile database file and page accounting")
	}
	return measurement
}

func TestOutputArchiveSQLiteSpaceUsage(t *testing.T) {
	for _, content := range []string{"text", "incompressible"} {
		t.Run(content, func(t *testing.T) {
			db, actor, job := scheduledJobFixture(t)
			if db.db.dialect != "sqlite" {
				t.Skip("SQLite page and file accounting")
			}
			ctx := context.Background()
			const size = 8 * 1024 * 1024
			data := make([]byte, 0, size+256)
			if content == "incompressible" {
				data = make([]byte, size)
				if _, err := rand.Read(data); err != nil {
					t.Fatal(err)
				}
			} else {
				for line := 0; len(data) < size; line++ {
					data = fmt.Appendf(data, "2026-11-25T12:%02d:%02dZ INFO host=fixture sequence=%08d result=ok\n", line/60%60, line%60, line)
				}
				data = data[:size]
			}
			writeRun := func(id string) {
				t.Helper()
				if _, _, _, err := db.CreateRun(ctx, actor, id, job.ID, job.Version); err != nil {
					t.Fatal(err)
				}
				for offset := 0; offset < len(data); offset += MaxOutputBatchBytes {
					if err := db.AppendOutput(ctx, id, "host", data[offset:min(offset+MaxOutputBatchBytes, len(data))]); err != nil {
						t.Fatal(err)
					}
				}
				if err := db.SetRunHost(ctx, id, "host", "succeeded", "", nil); err != nil {
					t.Fatal(err)
				}
				if err := db.FinishRun(ctx, id, "succeeded"); err != nil {
					t.Fatal(err)
				}
			}
			baseline := measureSQLiteOutputSpace(t, db)
			writeRun("archive")
			raw := measureSQLiteOutputSpace(t, db)
			if err := db.CompressRunOutput(ctx, "archive", "host"); err != nil {
				t.Fatal(err)
			}
			archived := measureSQLiteOutputSpace(t, db)
			var payloadBytes, rawRows int64
			if err := db.db.QueryRowContext(ctx, `SELECT SUM(LENGTH(data)) FROM run_output_archives WHERE run_id = 'archive'`).Scan(&payloadBytes); err != nil {
				t.Fatal(err)
			}
			if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_output WHERE run_id = 'archive'`).Scan(&rawRows); err != nil || rawRows != 0 {
				t.Fatalf("raw output remains: %d %v", rawRows, err)
			}
			if content == "text" {
				if payloadBytes >= size/10 || archived.UsedBytes-baseline.UsedBytes >= (raw.UsedBytes-baseline.UsedBytes)/10 {
					t.Fatal("smaller payload did not reduce occupied database space")
				}
				if archived.FreePages <= raw.FreePages || archived.DatabaseBytes < raw.DatabaseBytes {
					t.Fatal("default SQLite storage should retain freed pages for reuse")
				}
			} else if payloadBytes < size*98/100 || archived.UsedBytes < raw.UsedBytes/2 {
				t.Fatal("incompressible data misleadingly appeared to compress well")
			}
			// A second equally large log should reuse the space released by the first.
			writeRun("reuse")
			reused := measureSQLiteOutputSpace(t, db)
			if content == "text" && reused.DatabaseBytes > raw.DatabaseBytes+1024*1024 {
				t.Fatal("new output did not reuse freed database pages")
			}
			if err := db.DeleteCompletedRun(ctx, actor, "reuse"); err != nil {
				t.Fatal(err)
			}
			// Demonstrate physical reclamation separately, never imply compression
			// itself shrinks a SQLite file or run VACUUM in production automatically.
			if _, err := db.db.ExecContext(ctx, `VACUUM`); err != nil {
				t.Fatal(err)
			}
			compacted := measureSQLiteOutputSpace(t, db)
			if content == "text" && (compacted.DatabaseBytes >= raw.DatabaseBytes/10 || compacted.DatabaseAllocatedBytes >= raw.DatabaseAllocatedBytes/10) {
				t.Fatal("compacted file did not reflect compressed storage")
			}
			hash := sha256.New()
			for _, chunk := range readAllOutput(t, db, actor) {
				hash.Write(chunk.Data)
			}
			want := sha256.Sum256(data)
			if !bytes.Equal(hash.Sum(nil), want[:]) {
				t.Fatal("space reclamation changed archived output")
			}
			report := struct {
				Content      string                       `json:"content"`
				RawBytes     int64                        `json:"raw_payload_bytes"`
				ArchiveBytes int64                        `json:"archive_payload_bytes"`
				Stages       map[string]sqliteOutputSpace `json:"stages"`
			}{content, size, payloadBytes, map[string]sqliteOutputSpace{
				"baseline": baseline, "raw": raw, "archived": archived, "reused": reused, "compacted_fixture": compacted,
			}}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("Space measurements: %s", encoded)
		})
	}
}
