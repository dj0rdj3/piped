package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
)

func archiveFixture(t *testing.T) (*Store, Actor) {
	t.Helper()
	db, actor, job := scheduledJobFixture(t)
	if _, err := db.AdmitRun(context.Background(), actor, RunAdmission{ID: "archive", RequestID: "archive", JobID: job.ID, Version: job.Version}); err != nil {
		t.Fatal(err)
	}
	return db, actor
}

func readAllOutput(t *testing.T, db *Store, actor Actor) []OutputChunk {
	t.Helper()
	chunks := []OutputChunk{}
	var after int64
	for {
		page, err := db.RunOutput(context.Background(), actor, "archive", "host", after)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			return chunks
		}
		chunks = append(chunks, page...)
		after = page[len(page)-1].ID
	}
}

func TestOutputArchivePreservesPagesCursorsAndNotificationTail(t *testing.T) {
	db, actor := archiveFixture(t)
	ctx := context.Background()
	// Cross the database reader's 16-block boundary, preserve arbitrary binary
	// bytes, and leave a short last chunk so a history page splits another chunk.
	batch := bytes.Repeat([]byte("\x00\xff\xe2\x82\xac\x1b[31m\r\n"), 6000)[:MaxOutputBatchBytes]
	for range 65 {
		if err := db.AppendOutput(ctx, "archive", "host", batch); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AppendOutput(ctx, "archive", "host", []byte("end")); err != nil {
		t.Fatal(err)
	}
	want := readAllOutput(t, db, actor)
	tail, truncated, err := db.RunOutputTail(ctx, actor, "archive", "host")
	if err != nil || !truncated || tail[0].Offset == 0 {
		t.Fatalf("tail fixture: truncated=%v err=%v", truncated, err)
	}
	notification, err := db.NotificationOutput(ctx, "archive", "host")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CompressRunOutput(ctx, "archive", "host"); !errors.Is(err, ErrRunActive) {
		t.Fatalf("archived a live host: %v", err)
	}
	if err := db.SetRunHost(ctx, "archive", "host", "succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	// Pause after one committed block to exercise reads across both formats.
	if more, err := db.compressOutputBlock(ctx, "archive", "host"); err != nil || !more {
		t.Fatalf("first block: %v %v", more, err)
	}
	if got := readAllOutput(t, db, actor); !reflect.DeepEqual(got, want) {
		t.Fatal("mixed raw/compressed reads changed output or IDs")
	}
	if err := db.CompressRunOutput(ctx, "archive", "host"); err != nil {
		t.Fatal(err)
	}
	if err := db.CompressRunOutput(ctx, "archive", "host"); err != nil {
		t.Fatalf("compression is not idempotent: %v", err)
	}
	if got := readAllOutput(t, db, actor); !reflect.DeepEqual(got, want) {
		t.Fatal("compressed forward replay changed output or cursors")
	}
	gotTail, gotTruncated, err := db.RunOutputTail(ctx, actor, "archive", "host")
	if err != nil || gotTruncated != truncated || !reflect.DeepEqual(gotTail, tail) {
		t.Fatalf("compressed tail changed: %v", err)
	}
	gotNotification, err := db.NotificationOutput(ctx, "archive", "host")
	if err != nil || gotNotification != notification {
		t.Fatalf("compressed webhook tail changed: %v", err)
	}
	// Reassemble all older pages, including a cursor inside an archived chunk.
	pages := gotTail
	for gotTruncated {
		first := pages[0]
		older, more, err := db.RunOutputBefore(ctx, actor, "archive", "host", first.ID, first.Offset)
		if err != nil || len(older) == 0 {
			t.Fatalf("older compressed page: %v", err)
		}
		if older[len(older)-1].ID == first.ID {
			last := &older[len(older)-1]
			if last.Offset+len(last.Data) != first.Offset {
				t.Fatal("history boundary duplicated or lost bytes")
			}
			last.Data = append(last.Data, first.Data...)
			pages = pages[1:]
		}
		pages = append(older, pages...)
		gotTruncated = more
	}
	if !reflect.DeepEqual(pages, want) {
		t.Fatal("backward pagination changed bytes or IDs")
	}
	var raw, compressedBytes int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_output WHERE run_id = 'archive'`).Scan(&raw); err != nil || raw != 0 {
		t.Fatalf("uncompressed data remains: %d %v", raw, err)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT SUM(LENGTH(data)) FROM run_output_archives WHERE run_id = 'archive'`).Scan(&compressedBytes); err != nil || compressedBytes >= len(batch) {
		t.Fatalf("log was not compressed: %d %v", compressedBytes, err)
	}
	if err := db.AppendOutput(ctx, "archive", "host", []byte("late")); err == nil {
		t.Fatal("finished log accepted more output")
	}
	// Reading the tail or resuming a late cursor must not inflate older blocks.
	if _, err := db.db.ExecContext(ctx, `UPDATE run_output_archives SET data = ? WHERE run_id = 'archive' AND first_id = ?`, []byte("corrupt older block"), want[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.RunOutputTail(ctx, actor, "archive", "host"); err != nil {
		t.Fatalf("tail decompressed unrelated older output: %v", err)
	}
	if _, err := db.RunOutput(ctx, actor, "archive", "host", want[len(want)-20].ID); err != nil {
		t.Fatalf("cursor replay decompressed unrelated older output: %v", err)
	}
	if _, err := db.RunOutput(ctx, Actor{}, "archive", "host", 0); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("archive bypassed authorization: %v", err)
	}
	if err := db.FinishRun(ctx, "archive", "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := db.PurgeRuns(ctx, time.Now().Unix()+1); err != nil {
		t.Fatal(err)
	}
	var archives int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_output_archives WHERE run_id = 'archive'`).Scan(&archives); err != nil || archives != 0 {
		t.Fatalf("retention left archives behind: %d %v", archives, err)
	}
}

func TestOutputArchiveLeavesOtherRunningHostsUncompressed(t *testing.T) {
	db, actor, job := scheduledJobFixture(t)
	ctx := context.Background()
	if _, err := db.SaveHost(ctx, actor, Host{ID: "other", Name: "Other", Address: "localhost", Port: 22, Username: "fixture"}, []byte("password"), nil, true); err != nil {
		t.Fatal(err)
	}
	job.HostIDs = append(job.HostIDs, "other")
	job, err := db.SaveJob(ctx, actor, job, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AdmitRun(ctx, actor, RunAdmission{ID: "archive", RequestID: "archive", JobID: job.ID, Version: job.Version}); err != nil {
		t.Fatal(err)
	}
	// Global output IDs interleave between hosts; archives must retain those gaps.
	for range 66 {
		for _, hostID := range job.HostIDs {
			if err := db.AppendOutput(ctx, "archive", hostID, []byte(hostID)); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := readAllOutput(t, db, actor)
	if err := db.SetRunHost(ctx, "archive", "host", "failed", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.CompressCompletedOutput(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readAllOutput(t, db, actor); !reflect.DeepEqual(got, want) {
		t.Fatal("archive changed sparse output cursors")
	}
	var raw, archives int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_output WHERE run_id = 'archive' AND host_id = 'other'`).Scan(&raw); err != nil || raw != 66 {
		t.Fatalf("changed the active host's raw output: %d %v", raw, err)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_output_archives WHERE run_id = 'archive' AND host_id = 'other'`).Scan(&archives); err != nil || archives != 0 {
		t.Fatalf("compressed the active host's output: %d %v", archives, err)
	}
	if err := db.AppendOutput(ctx, "archive", "other", []byte("still live")); err != nil {
		t.Fatal(err)
	}
}

func TestOutputArchiveRecoverySmallWritesAndConcurrentReaders(t *testing.T) {
	db, actor := archiveFixture(t)
	ctx := context.Background()
	for range 1100 {
		if err := db.AppendOutput(ctx, "archive", "host", []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	want := readAllOutput(t, db, actor)
	if err := db.RecoverRuns(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := db.CompressCompletedOutput(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	for range 3 {
		got, more, err := db.RunOutputTail(ctx, actor, "archive", "host")
		if err != nil || more || !reflect.DeepEqual(got, want) {
			t.Error("tail changed during concurrent compression", err)
		}
	}
	wg.Wait()
	got, more, err := db.RunOutputTail(ctx, actor, "archive", "host")
	if err != nil || more || !reflect.DeepEqual(got, want) {
		t.Fatalf("small-write archive truncated: %v", err)
	}
	if _, _, err := db.RunOutputBefore(ctx, actor, "archive", "host", want[0].ID, 2); !errors.Is(err, ErrOutputCursor) {
		t.Fatalf("invalid byte offset accepted: %v", err)
	}
	if err := db.DeleteCompletedRun(ctx, actor, "archive"); err != nil {
		t.Fatal(err)
	}
	if err := db.CompressRunOutput(ctx, "archive", "host"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing host accepted: %v", err)
	}
}

func TestOutputArchiveRollbackAndCorruption(t *testing.T) {
	db, actor := archiveFixture(t)
	ctx := context.Background()
	if err := db.AppendOutput(ctx, "archive", "host", []byte("preserved output")); err != nil {
		t.Fatal(err)
	}
	want := readAllOutput(t, db, actor)
	if err := db.SetRunHost(ctx, "archive", "host", "cancelled", "", nil); err != nil {
		t.Fatal(err)
	}
	// Inserting an archive must roll back if deleting the raw rows fails.
	reject := `CREATE TRIGGER reject_archive_delete BEFORE DELETE ON run_output BEGIN SELECT RAISE(ABORT, 'fixture'); END`
	remove := `DROP TRIGGER reject_archive_delete`
	if db.db.dialect != "sqlite" {
		// A portable foreign key prevents deleting a referenced output row.
		reject = `CREATE TABLE archive_delete_guard (output_id BIGINT REFERENCES run_output(id))`
		if db.db.dialect == "mysql" || db.db.dialect == "mariadb" {
			reject = `CREATE TABLE archive_delete_guard (output_id BIGINT, FOREIGN KEY(output_id) REFERENCES run_output(id)) ENGINE=InnoDB`
		}
		remove = `DROP TABLE archive_delete_guard`
	}
	if _, err := db.db.ExecContext(ctx, reject); err != nil {
		t.Fatal(err)
	}
	if db.db.dialect != "sqlite" {
		if _, err := db.db.ExecContext(ctx, `INSERT INTO archive_delete_guard(output_id) VALUES (?)`, want[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CompressRunOutput(ctx, "archive", "host"); err == nil {
		t.Fatal("fixture did not reject deletion")
	}
	if got := readAllOutput(t, db, actor); !reflect.DeepEqual(got, want) {
		t.Fatal("failed replacement changed output")
	}
	if _, err := db.db.ExecContext(ctx, remove); err != nil {
		t.Fatal(err)
	}
	if err := db.CompressRunOutput(ctx, "archive", "host"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE run_output_archives SET data = ? WHERE run_id = 'archive'`, []byte("corrupt")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RunOutput(ctx, actor, "archive", "host", 0); err == nil {
		t.Fatal("corrupt compressed data was silently accepted")
	}
}

func TestDecodeOutputArchiveRejectsTruncationFramingAndExpansion(t *testing.T) {
	valid, err := encodeOutputBlock([]OutputChunk{{ID: 2, Data: []byte("data")}})
	if err != nil {
		t.Fatal(err)
	}
	var bomb bytes.Buffer
	writer := gzip.NewWriter(&bomb)
	if _, err := writer.Write(make([]byte, 1024*1024)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	checksum := bytes.Clone(valid)
	checksum[len(checksum)-8] ^= 1
	var empty bytes.Buffer
	if err := gzip.NewWriter(&empty).Close(); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{
		valid[:len(valid)-1], checksum, bomb.Bytes(),
		append(bytes.Clone(valid), empty.Bytes()...),
		append(bytes.Clone(valid), 0),
		make([]byte, maxArchiveBytes+1),
	} {
		if _, err := decodeOutputBlock(data, 2, 2); err == nil {
			t.Fatal("invalid compressed stream accepted")
		}
	}
	if _, err := decodeOutputBlock(valid, 1, 2); err == nil {
		t.Fatal("wrong block boundaries accepted")
	}
}

func TestOutputArchiveMaintenanceContinuesPastDamagedOutput(t *testing.T) {
	db, actor, job := scheduledJobFixture(t)
	ctx := context.Background()
	for _, id := range []string{"broken", "later"} {
		if _, err := db.AdmitRun(ctx, actor, RunAdmission{ID: id, RequestID: id, JobID: job.ID, Version: job.Version}); err != nil {
			t.Fatal(err)
		}
		if err := db.AppendOutput(ctx, id, "host", []byte("preserved")); err != nil {
			t.Fatal(err)
		}
		if err := db.SetRunHost(ctx, id, "host", "succeeded", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE run_output SET data = ? WHERE run_id = 'broken'`, make([]byte, 4097)); err != nil {
		t.Fatal(err)
	}
	if err := db.CompressCompletedOutput(ctx); err == nil {
		t.Fatal("damaged output was silently accepted")
	}
	var broken, later int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_output WHERE run_id = 'broken'`).Scan(&broken); err != nil || broken != 1 {
		t.Fatalf("damaged source was removed: %d %v", broken, err)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_output WHERE run_id = 'later'`).Scan(&later); err != nil || later != 0 {
		t.Fatalf("damaged output blocked another host: %d %v", later, err)
	}
	if _, err := db.RunOutput(ctx, actor, "broken", "host", 0); err == nil {
		t.Fatal("oversized raw output was returned")
	}
	chunks, _, err := db.RunOutputTail(ctx, actor, "later", "host")
	if err != nil || len(chunks) != 1 || string(chunks[0].Data) != "preserved" {
		t.Fatalf("later output was damaged: %v", err)
	}
	for _, data := range [][]byte{{}, make([]byte, maxArchiveBytes+1)} {
		if _, err := db.db.ExecContext(ctx, `UPDATE run_output_archives SET data = ? WHERE run_id = 'later'`, data); err != nil {
			t.Fatal(err)
		}
		if _, _, err := db.RunOutputTail(ctx, actor, "later", "host"); err == nil {
			t.Fatal("invalid archive was treated as the end of output")
		}
		if _, err := db.RunOutput(ctx, actor, "later", "host", 0); err == nil || errors.Is(err, io.EOF) {
			t.Fatal("invalid archive was treated as the end of output")
		}
		if _, err := db.NotificationOutput(ctx, "later", "host"); err == nil {
			t.Fatal("invalid archive was silently used for notification output")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := db.CompressCompletedOutput(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("maintenance ignored cancellation: %v", err)
	}
}

func TestOutputArchiveTailDoesNotInflateAnOlderBlockForLookahead(t *testing.T) {
	db, actor := archiveFixture(t)
	ctx := context.Background()
	for _, value := range []byte{'x', 'y'} {
		for range 4 {
			if err := db.AppendOutput(ctx, "archive", "host", bytes.Repeat([]byte{value}, MaxOutputBatchBytes)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.SetRunHost(ctx, "archive", "host", "succeeded", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.CompressRunOutput(ctx, "archive", "host"); err != nil {
		t.Fatal(err)
	}
	var first int64
	if err := db.db.QueryRowContext(ctx, `SELECT MIN(first_id) FROM run_output_archives WHERE run_id = 'archive'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE run_output_archives SET data = ? WHERE run_id = 'archive' AND first_id = ?`, []byte{}, first); err != nil {
		t.Fatal(err)
	}
	chunks, more, err := db.RunOutputTail(ctx, actor, "archive", "host")
	if err != nil || !more || len(chunks) != 64 {
		t.Fatalf("unneeded lookahead hid a healthy tail: %v", err)
	}
	want := bytes.Repeat([]byte{'y'}, 256*1024)
	output, err := db.NotificationOutput(ctx, "archive", "host")
	if err != nil || !bytes.Equal([]byte(output), want) {
		t.Fatalf("unneeded lookahead broke notification output: %v", err)
	}
	if _, _, err := db.RunOutputBefore(ctx, actor, "archive", "host", chunks[0].ID, 0); err == nil {
		t.Fatal("reading the damaged block did not report corruption")
	}
}

func TestOutputArchivePartialFirstChunkStillHasEarlierBytes(t *testing.T) {
	db, actor := archiveFixture(t)
	ctx := context.Background()
	for range 4 {
		if err := db.AppendOutput(ctx, "archive", "host", bytes.Repeat([]byte{'x'}, MaxOutputBatchBytes)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AppendOutput(ctx, "archive", "host", []byte("y")); err != nil {
		t.Fatal(err)
	}
	for _, compressed := range []bool{false, true} {
		if compressed {
			if err := db.SetRunHost(ctx, "archive", "host", "succeeded", "", nil); err != nil {
				t.Fatal(err)
			}
			if err := db.CompressRunOutput(ctx, "archive", "host"); err != nil {
				t.Fatal(err)
			}
		}
		tail, more, err := db.RunOutputTail(ctx, actor, "archive", "host")
		if err != nil || !more || tail[0].Offset != 1 {
			t.Fatalf("first chunk prefix hidden (compressed=%v): more=%v err=%v", compressed, more, err)
		}
		prefix, more, err := db.RunOutputBefore(ctx, actor, "archive", "host", tail[0].ID, tail[0].Offset)
		if err != nil || more || len(prefix) != 1 || string(prefix[0].Data) != "x" {
			t.Fatalf("first byte was lost (compressed=%v): %v", compressed, err)
		}
	}
}
