package store

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"
)

// These benchmarks use the production store and its real WAL/FULL commits on
// disk. Run the same file against both drivers, without the race detector.
func benchmarkRun(b *testing.B) (*Store, Actor) {
	b.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(b.TempDir(), "data", "piped.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	user, err := s.InitAdmin(ctx, "admin", "benchmark-hash")
	if err != nil {
		b.Fatal(err)
	}
	if err := s.CreateSession(ctx, user, "session", time.Now().Unix(), time.Now().Add(time.Hour).Unix()); err != nil {
		b.Fatal(err)
	}
	actor := Actor{User: user, SessionHash: "session"}
	if _, err := s.SaveHost(ctx, actor, Host{ID: "host", Name: "Host", Address: "localhost", Port: 22, Username: "user"}, []byte("encrypted"), nil, true); err != nil {
		b.Fatal(err)
	}
	job, err := s.SaveJob(ctx, actor, Job{ID: "job", Name: "Output", Script: "echo test", HostIDs: []string{"host"}, ExecutionMode: "sequential"}, nil, true)
	if err != nil {
		b.Fatal(err)
	}
	if _, _, _, err := s.CreateRun(ctx, actor, "run", job.ID, job.Version); err != nil {
		b.Fatal(err)
	}
	return s, actor
}

func BenchmarkDurableOutput(b *testing.B) {
	for _, size := range []int{4096, MaxOutputBatchBytes} {
		name := "4KiB"
		if size == MaxOutputBatchBytes {
			name = "64KiB"
		}
		b.Run(name, func(b *testing.B) {
			s, _ := benchmarkRun(b)
			data := bytes.Repeat([]byte("x"), size)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				if err := s.AppendOutput(context.Background(), "run", "host", data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkConcurrentOutput(b *testing.B) {
	s, _ := benchmarkRun(b)
	data := bytes.Repeat([]byte("x"), 4096)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	// With -cpu=2 this creates ten contending writers, matching the supported
	// simultaneous-job workload while retaining the production connection limit.
	b.SetParallelism(5)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := s.AppendOutput(context.Background(), "run", "host", data); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

func BenchmarkHistoryPage(b *testing.B) {
	s, actor := benchmarkRun(b)
	data := bytes.Repeat([]byte("x"), MaxOutputBatchBytes)
	for range 8 {
		if err := s.AppendOutput(context.Background(), "run", "host", data); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.SetBytes(MaxOutputBatchBytes)
	for b.Loop() {
		chunks, err := s.RunOutput(context.Background(), actor, "run", "host", 0)
		if err != nil || len(chunks) != 16 {
			b.Fatalf("history page: %d chunks, %v", len(chunks), err)
		}
	}
}
