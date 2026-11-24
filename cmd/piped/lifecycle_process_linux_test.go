package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"piped/internal/hosts"
	"piped/internal/jobs"
	"piped/internal/secrets"
	"piped/internal/store"
)

type startupFixture struct {
	directory string
	database  string
	address   string
	actor     store.Actor
	vault     *secrets.Vault
	job       store.Job
}

func pendingStartupFixture(t *testing.T, address string) startupFixture {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	if err := secrets.GenerateEnv(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "127.0.0.1:8080", address)), 0600); err != nil {
		t.Fatal(err)
	}
	configuration, err := secrets.LoadConfiguration(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture := startupFixture{directory: directory, database: filepath.Join(directory, "data", "piped.db"), address: address, vault: configuration.Vault}
	db, err := store.Open(context.Background(), fixture.database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	user, err := db.InitAdmin(ctx, "admin", "fixture-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, user, "fixture-session", time.Now().Unix(), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	fixture.actor = store.Actor{User: user, SessionHash: "fixture-session"}
	hostService, err := hosts.New(ctx, db, fixture.vault)
	if err != nil {
		t.Fatal(err)
	}
	password := "fixture-password"
	host, err := hostService.Save(ctx, fixture.actor, "", hosts.Input{Name: "Fixture", Address: "127.0.0.1", Port: 1, Username: "fixture", Password: &password})
	if err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.New(ctx, db, fixture.vault)
	if err != nil {
		t.Fatal(err)
	}
	fixture.job, err = jobService.Save(ctx, fixture.actor, "", jobs.Input{Name: "Fixture", Script: "echo should-not-replay", HostIDs: []string{host.ID}, ExecutionMode: "sequential"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.CreateRun(ctx, fixture.actor, "unfinished", fixture.job.ID, fixture.job.Version); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestStartupFailuresDoNotRecoverUnfinishedRuns(t *testing.T) {
	for _, kind := range []string{"occupied listener", "wrong vault"} {
		t.Run(kind, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			fixture := pendingStartupFixture(t, listener.Addr().String())
			path := filepath.Join(fixture.directory, ".env")
			if kind == "wrong vault" {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				_, settings, _ := strings.Cut(string(data), "\n")
				wrong := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{83}, 32))
				if err := os.WriteFile(path, []byte("PIPED_ENCRYPTION_KEY="+wrong+"\n"+settings), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(fixture.directory)
			var output bytes.Buffer
			if err := run(nil, strings.NewReader(""), &output); err == nil {
				t.Fatal("startup unexpectedly succeeded")
			}
			if strings.Contains(output.String(), "Piped listening") {
				t.Fatal("failed startup announced readiness")
			}
			db, err := store.Open(context.Background(), fixture.database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			run, err := db.Run(context.Background(), fixture.actor, "unfinished")
			if err != nil || run.Status != "running" || run.FinishedAt != 0 || len(run.Hosts) != 1 || run.Hosts[0].FinishedAt != 0 {
				t.Fatalf("failed startup modified unfinished work: %+v, %v", run, err)
			}
		})
	}
}

func TestStartupProcessHelper(t *testing.T) {
	if os.Getenv("PIPED_TEST_STARTUP_PROCESS") != "1" {
		return
	}
	if err := run(nil, strings.NewReader(""), io.Discard); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestServerSignalsRecoverWithoutReplayAndReleaseDatabase(t *testing.T) {
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT} {
		t.Run(signal.String(), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			listener.Close()
			fixture := pendingStartupFixture(t, address)
			path := filepath.Join(fixture.directory, ".env")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestStartupProcessHelper$")
			command.Env = append(os.Environ(), "PIPED_TEST_STARTUP_PROCESS=1")
			command.Dir = fixture.directory
			command.Stdout, command.Stderr = io.Discard, io.Discard
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var processErr error
			go func() {
				defer close(done)
				processErr = command.Wait()
			}()
			defer func() {
				command.Process.Kill()
				<-done
			}()
			client := &http.Client{Timeout: 200 * time.Millisecond}
			deadline := time.Now().Add(10 * time.Second)
			for {
				select {
				case <-done:
					t.Fatalf("startup process exited: %v", processErr)
				default:
				}
				response, err := client.Get("http://" + address + "/api/health")
				if err == nil {
					response.Body.Close()
					if response.StatusCode == 200 {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatal("startup did not become ready")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := command.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("signal shutdown did not finish")
			}
			if processErr != nil {
				t.Fatalf("signal shutdown failed: %v", processErr)
			}
			connection, err := net.DialTimeout("tcp", address, time.Second)
			if err == nil {
				connection.Close()
				t.Fatal("shutdown left a listener open")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("startup/shutdown changed deployment configuration")
			}
			db, err := store.Open(context.Background(), fixture.database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.VerifyVault(context.Background(), fixture.vault); err != nil {
				t.Fatal(err)
			}
			runs, err := db.Runs(context.Background(), fixture.actor, fixture.job.ID)
			if err != nil || len(runs) != 1 || runs[0].Status != "failed" || runs[0].FinishedAt == 0 {
				t.Fatalf("recovery replayed or failed to finish the old run: %+v, %v", runs, err)
			}
		})
	}
}
