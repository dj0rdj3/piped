package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/store"
)

func TestSQLiteAutomaticallyReclaimsSpaceWhileAnotherHostWrites(t *testing.T) {
	if os.Getenv("PIPED_TEST_DATABASE_CONFIG") != "" {
		t.Skip("SQLite background reclamation")
	}
	f := newFixture(t, "parallel", false)
	ctx := context.Background()
	f.job.HostIDs = f.job.HostIDs[:2]
	var err error
	f.job, err = f.db.SaveJob(ctx, f.actor, f.job, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	written := make(chan struct{})
	finishFirst := make(chan struct{}, 1)
	finishWriter := make(chan struct{}, 1)
	defer close(finishFirst)
	defer close(finishWriter)
	var writes, maxWriteNanos atomic.Int64
	batch := bytes.Repeat([]byte("example output line\n"), 4000)[:store.MaxOutputBatchBytes]
	const batches = 128
	f.engine.runner = func(ctx context.Context, c store.Connection, _ string, output io.Writer, ready func(io.WriteCloser), _ func(ssh.PublicKey) error) (*int, error) {
		ready(nil)
		code := 0
		if c.Host.ID == f.hosts[0].ID {
			for range batches {
				if _, err := output.Write(batch); err != nil {
					return nil, err
				}
			}
			close(written)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-finishFirst:
				return &code, nil
			}
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-finishWriter:
				return &code, nil
			case <-ticker.C:
				started := time.Now()
				if _, err := fmt.Fprintf(output, "tick %d\n", writes.Load()); err != nil {
					return nil, err
				}
				elapsed := time.Since(started).Nanoseconds()
				for old := maxWriteNanos.Load(); elapsed > old; old = maxWriteNanos.Load() {
					if maxWriteNanos.CompareAndSwap(old, elapsed) {
						break
					}
				}
				writes.Add(1)
			}
		}
	}
	run := startFixture(t, f)
	select {
	case <-written:
	case <-time.After(10 * time.Second):
		t.Fatal("large log was not persisted")
	}
	fileSpace := func() (int64, int64, int64) {
		t.Helper()
		info, err := os.Stat(f.databasePath)
		if err != nil {
			t.Fatal(err)
		}
		var wal int64
		if info, err := os.Stat(f.databasePath + "-wal"); err == nil {
			wal = info.Sys().(*syscall.Stat_t).Blocks * 512
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return info.Size(), info.Sys().(*syscall.Stat_t).Blocks * 512, wal
	}
	beforeBytes, beforeAllocated, _ := fileSpace()
	if beforeBytes < 4*1024*1024 {
		t.Fatal("fixture did not allocate a large database")
	}
	beforeWrites := writes.Load()
	// Read page accounting without test-side checkpointing. A growing live log
	// changes the minimum database size, so verify the freelist drains instead
	// of requiring a fixed compression ratio for the entire database file.
	uri := url.URL{Scheme: "file", Path: f.databasePath}
	query := uri.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(100)")
	uri.RawQuery = query.Encode()
	accounting, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer accounting.Close()
	accounting.SetMaxOpenConns(1)
	started := time.Now()
	finishFirst <- struct{}{}
	var afterBytes, afterAllocated, afterWAL int64
	var freePages int64
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		afterBytes, afterAllocated, afterWAL = fileSpace()
		if err := accounting.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
			t.Fatal(err)
		}
		if freePages == 0 && afterAllocated+afterWAL < beforeAllocated/4 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if freePages != 0 || afterAllocated+afterWAL >= beforeAllocated/4 {
		t.Fatalf("automatic reclamation did not return disk space: db=%d allocated=%d WAL=%d free_pages=%d before=%d", afterBytes, afterAllocated, afterWAL, freePages, beforeAllocated)
	}
	reclaimElapsed := time.Since(started)
	progress := writes.Load() - beforeWrites
	current, err := f.db.Run(ctx, f.actor, run.ID)
	if err != nil || current.FinishedAt != 0 || current.Hosts[1].FinishedAt != 0 || progress < 10 {
		t.Fatalf("reclamation stopped another host: writes=%d err=%v", progress, err)
	}
	if time.Duration(maxWriteNanos.Load()) > time.Second {
		t.Fatalf("foreground persistence stalled: %s", time.Duration(maxWriteNanos.Load()))
	}
	// Continue beyond the first successful vacuum/checkpoint. Later log writes
	// must not leave a growing WAL behind after the freelist has drained.
	continuedFrom := writes.Load()
	await(t, func() bool { return writes.Load()-continuedFrom >= 400 })
	deadline = time.Now().Add(3 * time.Second)
	var sustainedAllocated, sustainedWAL int64
	for time.Now().Before(deadline) {
		_, sustainedAllocated, sustainedWAL = fileSpace()
		if sustainedAllocated+sustainedWAL < beforeAllocated/2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if sustainedAllocated+sustainedWAL >= beforeAllocated/2 {
		t.Fatalf("continued logging retained reclaimed disk space: db=%d WAL=%d before=%d", sustainedAllocated, sustainedWAL, beforeAllocated)
	}
	// No test-side vacuum or checkpoint contributes to the observed shrinkage.
	hash := sha256.New()
	var after int64
	for {
		chunks, err := f.db.RunOutput(ctx, f.actor, run.ID, f.hosts[0].ID, after)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunks) == 0 {
			break
		}
		for _, chunk := range chunks {
			hash.Write(chunk.Data)
			after = chunk.ID
		}
	}
	want := sha256.New()
	for range batches {
		want.Write(batch)
	}
	if !bytes.Equal(hash.Sum(nil), want.Sum(nil)) {
		t.Fatal("reclamation changed archived output")
	}
	finishWriter <- struct{}{}
	if result := finished(t, f, run.ID); result.Status != "succeeded" {
		t.Fatalf("reclamation changed job outcome: %s", result.Status)
	}
	var logged bytes.Buffer
	after = 0
	for {
		chunks, err := f.db.RunOutput(ctx, f.actor, run.ID, f.hosts[1].ID, after)
		if err != nil {
			t.Fatal(err)
		}
		if len(chunks) == 0 {
			break
		}
		for _, chunk := range chunks {
			logged.Write(chunk.Data)
			after = chunk.ID
		}
	}
	var expected bytes.Buffer
	for tick := int64(0); tick < writes.Load(); tick++ {
		fmt.Fprintf(&expected, "tick %d\n", tick)
	}
	if !bytes.Equal(logged.Bytes(), expected.Bytes()) {
		t.Fatal("reclamation lost or changed the concurrently written log")
	}
	report := map[string]any{
		"database_bytes_before": beforeBytes, "database_bytes_after": afterBytes,
		"database_allocated_before": beforeAllocated, "database_allocated_after": afterAllocated,
		"wal_allocated_after": afterWAL, "concurrent_writes": progress,
		"max_write_milliseconds":         float64(maxWriteNanos.Load()) / 1e6,
		"reclaim_milliseconds":           float64(reclaimElapsed.Milliseconds()),
		"test_side_vacuum_or_checkpoint": false,
		"sustained_database_allocated":   sustainedAllocated,
		"sustained_wal_allocated":        sustainedWAL,
		"continued_writes":               writes.Load() - continuedFrom,
		"concurrent_log_byte_exact":      true,
		"free_pages_after":               freePages,
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Automatic reclamation: %s", encoded)
}
