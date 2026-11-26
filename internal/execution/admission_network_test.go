package execution

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/ssh"
	"piped/internal/jobs"
	"piped/internal/store"
	"piped/internal/testdatabase"
)

// Drop exactly one admission COMMIT, either before it reaches the disposable
// server or after the server confirms success. Other traffic remains intact.
func admissionCommitProxy(t *testing.T, driver, dsn string, afterCommit bool) (store.Config, <-chan struct{}) {
	t.Helper()
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil || parsed.Net != "tcp" || parsed.TLSConfig != "" {
		t.Fatal("admission fault requires disposable unencrypted TCP MySQL/MariaDB")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	destination := parsed.Addr
	parsed.Addr = listener.Addr().String()
	dropped := make(chan struct{})
	var injected atomic.Bool
	var workers sync.WaitGroup
	var mu sync.Mutex
	var connections []net.Conn
	stopped := false
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			server, err := net.DialTimeout("tcp", destination, time.Second)
			if err != nil {
				client.Close()
				return
			}
			mu.Lock()
			if stopped {
				mu.Unlock()
				client.Close()
				server.Close()
				return
			}
			connections = append(connections, client, server)
			mu.Unlock()
			var awaitingCommit atomic.Bool
			workers.Add(2)
			go func() {
				defer workers.Done()
				defer client.Close()
				defer server.Close()
				admission := false
				for {
					packet, err := mysqlPacket(client)
					if err != nil {
						return
					}
					payload := packet[4:]
					if len(payload) > 1 && payload[0] == 0x16 && strings.Contains(strings.ToUpper(string(payload[1:])), "INSERT INTO RUNS") {
						admission = true
					}
					if admission && len(payload) > 1 && payload[0] == 0x03 && strings.EqualFold(string(payload[1:]), "COMMIT") && injected.CompareAndSwap(false, true) {
						if !afterCommit {
							close(dropped)
							return // Disconnect rolls back the still-open transaction.
						}
						awaitingCommit.Store(true)
					}
					if _, err := server.Write(packet); err != nil {
						return
					}
				}
			}()
			go func() {
				defer workers.Done()
				defer client.Close()
				defer server.Close()
				for {
					packet, err := mysqlPacket(server)
					if err != nil {
						return
					}
					if awaitingCommit.Load() {
						if len(packet) < 5 || packet[4] != 0x00 {
							t.Error("admission COMMIT did not receive a server success reply")
						}
						close(dropped)
						return // Durable commit, but its success cannot reach the client.
					}
					if _, err := client.Write(packet); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		stopped = true
		for _, connection := range connections {
			connection.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return store.Config{Driver: driver, DSN: parsed.FormatDSN()}, dropped
}

func mysqlPacket(connection net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	size := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	packet := make([]byte, 4+size)
	copy(packet, header)
	_, err := io.ReadFull(connection, packet[4:])
	return packet, err
}

func TestAdmissionCommitDisconnectDoesNotStrandOrReplayRuns(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		for _, afterCommit := range []bool{false, true} {
			name := map[bool]string{false: "manual", true: "scheduled"}[scheduled] + "/" + map[bool]string{false: "rolled_back", true: "durably_committed"}[afterCommit]
			t.Run(name, func(t *testing.T) {
				driver, dsn := testdatabase.Settings(t)
				if driver != "mysql" && driver != "mariadb" {
					t.Skip("requires disposable MySQL/MariaDB wire protocol")
				}
				configuration, dropped := admissionCommitProxy(t, driver, dsn, afterCommit)
				f := newFixtureWithDatabase(t, "parallel", false, configuration)
				job, err := jobService(t, f.db, f.vault).Save(context.Background(), f.actor, f.job.ID, jobs.Input{
					Name: f.job.Name, Script: f.job.Script, HostIDs: f.job.HostIDs,
					ExecutionMode: "parallel", Version: f.job.Version,
					Schedule: &store.JobSchedule{Cron: "* * * * *", Enabled: true},
				})
				if err != nil {
					t.Fatal(err)
				}
				f.job = job
				var clock atomic.Int64
				at := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
				clock.Store(at.Unix())
				f.engine.mu.Lock()
				f.engine.now = func() time.Time { return time.Unix(clock.Load(), 0) }
				f.engine.mu.Unlock()
				var calls atomic.Int32
				f.engine.runner = func(context.Context, store.Connection, string, io.Writer, func(io.WriteCloser), func(ssh.PublicKey) error) (*int, error) {
					calls.Add(1)
					code := 0
					return &code, nil
				}
				if scheduled {
					f.engine.runSchedules(at)
				} else if _, err := f.engine.Start(context.Background(), f.actor, job.ID, job.Version); err == nil {
					t.Fatal("lost commit acknowledgement was reported as successful admission")
				}
				select {
				case <-dropped:
				case <-time.After(time.Second):
					t.Fatal("did not drop the exact admission COMMIT")
				}
				await(t, func() bool {
					f.engine.mu.Lock()
					defer f.engine.mu.Unlock()
					return len(f.engine.active) == 0
				})
				runs := schedulerRuns(t, f)
				if afterCommit {
					if len(runs) != 1 || runs[0].Status != "failed" || runs[0].FinishedAt == 0 {
						t.Fatal("durably admitted run was stranded after lost COMMIT reply", runs)
					}
					current := finished(t, f, runs[0].ID)
					for _, host := range current.Hosts {
						if host.Status != "failed" || host.FinishedAt == 0 || !strings.Contains(host.Message, "script was not started") {
							t.Fatal("unlaunched host was not reconciled", host)
						}
					}
				} else if len(runs) != 0 {
					t.Fatal("rolled back admission created history", runs)
				}
				if calls.Load() != 0 {
					t.Fatal("ambiguous admission executed or replayed SSH", calls.Load())
				}
				// A later cron occurrence must run normally, proving reconciliation
				// removed the unfinished-row overlap block without restarting.
				clock.Store(at.Add(time.Minute).Unix())
				f.engine.runSchedules(at.Add(time.Minute))
				runs = schedulerRuns(t, f)
				expectedRuns := 1
				if afterCommit {
					expectedRuns++
				}
				if len(runs) != expectedRuns {
					t.Fatal("later occurrence was blocked by ambiguous admission", runs)
				}
				if current := finished(t, f, runs[0].ID); current.Status != "succeeded" || calls.Load() != int32(len(f.hosts)) {
					t.Fatal("later occurrence did not execute exactly once", current, calls.Load())
				}
			})
		}
	}
}
