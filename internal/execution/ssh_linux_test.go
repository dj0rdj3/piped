//go:build linux

package execution

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
	"piped/internal/credentials"
	"piped/internal/hosts"
	"piped/internal/jobs"
)

// This SSH fixture runs the real remote supervisor in a Linux PTY. It verifies
// SSH transport, terminal input, host-key gating, and OS process-group cleanup.
type sshFixture struct {
	listener        net.Listener
	signer          ssh.Signer
	authentications atomic.Int32
	mu              sync.Mutex
	conns           []net.Conn
	wg              sync.WaitGroup
	environment     []string
	commands        []string
}

func newSSHFixture(t *testing.T, public ssh.PublicKey) *sshFixture {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &sshFixture{listener: listener, signer: signer}
	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			fixture.authentications.Add(1)
			if string(password) != "fixture-ssh-secret" {
				return nil, errors.New("invalid password")
			}
			return nil, nil
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			fixture.authentications.Add(1)
			if public == nil || string(key.Marshal()) != string(public.Marshal()) {
				return nil, errors.New("invalid key")
			}
			return nil, nil
		},
	}
	config.AddHostKey(signer)
	fixture.wg.Add(1)
	go func() {
		defer fixture.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			fixture.mu.Lock()
			fixture.conns = append(fixture.conns, conn)
			fixture.mu.Unlock()
			fixture.wg.Add(1)
			go func() {
				defer fixture.wg.Done()
				defer conn.Close()
				transport, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer transport.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						_ = incoming.Reject(ssh.UnknownChannelType, "session required")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						return
					}
					fixture.wg.Add(1)
					fixture.mu.Lock()
					environment := append([]string(nil), fixture.environment...)
					fixture.mu.Unlock()
					go func() {
						defer fixture.wg.Done()
						serveTerminal(channel, requests, environment, func(command string) {
							fixture.mu.Lock()
							fixture.commands = append(fixture.commands, command)
							fixture.mu.Unlock()
						})
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		fixture.mu.Lock()
		for _, conn := range fixture.conns {
			conn.Close()
		}
		fixture.mu.Unlock()
		fixture.wg.Wait()
	})
	return fixture
}

func terminalPair() (*os.File, *os.File, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		return nil, nil, err
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	termios, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		master.Close()
		slave.Close()
		return nil, nil, err
	}
	termios.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, termios); err != nil {
		master.Close()
		slave.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

func serveTerminal(channel ssh.Channel, requests <-chan *ssh.Request, environment []string, record func(string)) {
	defer channel.Close()
	master, slave, err := terminalPair()
	if err != nil {
		return
	}
	defer master.Close()
	defer slave.Close()
	var command *exec.Cmd
	ptyRequested := false
	for request := range requests {
		switch request.Type {
		case "pty-req":
			ptyRequested = true
			_ = request.Reply(true, nil)
		case "exec":
			var payload struct{ Command string }
			if ssh.Unmarshal(request.Payload, &payload) != nil || command != nil {
				_ = request.Reply(false, nil)
				continue
			}
			record(payload.Command)
			command = exec.Command("bash", "-c", payload.Command)
			if environment != nil {
				command.Env = environment
			}
			if !ptyRequested {
				command.Stdin, command.Stdout, command.Stderr = channel, channel, channel.Stderr()
				if command.Start() != nil {
					_ = request.Reply(false, nil)
					return
				}
				_ = request.Reply(true, nil)
				err := command.Wait()
				code := uint32(0)
				if err != nil {
					code = 1
				}
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
				return
			}
			command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
			command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if command.Start() != nil {
				_ = request.Reply(false, nil)
				return
			}
			_ = request.Reply(true, nil)
			slave.Close()
			copied := make(chan struct{})
			go func() { _, _ = io.Copy(channel, master); close(copied) }()
			go func() { _, _ = io.Copy(master, channel); master.Close(); _ = command.Process.Signal(syscall.SIGHUP) }()
			err := command.Wait()
			<-copied
			code := uint32(0)
			if err != nil {
				code = 1
				if exit, ok := err.(*exec.ExitError); ok {
					code = uint32(exit.ExitCode())
				}
			}
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{code}))
			return
		default:
			_ = request.Reply(false, nil)
		}
	}
}

func targetSSH(t *testing.T, f *engineFixture, server *sshFixture, script string, sudo bool, password *string, credentialID string) {
	t.Helper()
	ctx := context.Background()
	service, err := hosts.New(ctx, f.db, f.vault)
	if err != nil {
		t.Fatal(err)
	}
	_, portString, _ := net.SplitHostPort(server.listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	target, err := service.Save(ctx, f.actor, f.hosts[0].ID, hosts.Input{Name: "First", Address: "127.0.0.1", Port: port, Username: "fixture", RunAsSudo: sudo, SudoPassword: password, CredentialID: credentialID, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	f.hosts[0] = target
	job, err := jobService(t, f.db, f.vault).Save(ctx, f.actor, f.job.ID, jobs.Input{Name: f.job.Name, Script: script, HostIDs: []string{target.ID}, ExecutionMode: "sequential", RunAsSudo: sudo, StopOnFailure: true, Version: f.job.Version})
	if err != nil {
		t.Fatal(err)
	}
	f.job = job
}
func pendingKey(t *testing.T, f *engineFixture, runID string) HostKey {
	t.Helper()
	var keys []HostKey
	await(t, func() bool {
		var err error
		keys, err = f.engine.Pending(context.Background(), f.actor, runID)
		if err != nil {
			t.Fatal(err)
		}
		return len(keys) > 0
	})
	return keys[0]
}
func acceptKey(t *testing.T, f *engineFixture, runID string) HostKey {
	t.Helper()
	key := pendingKey(t, f, runID)
	if err := f.engine.Approve(context.Background(), f.actor, ApprovalRequest{RunID: runID, HostID: key.HostID, Fingerprint: key.Fingerprint, Accept: true}); err != nil {
		t.Fatal(err)
	}
	return key
}
func outputText(t *testing.T, f *engineFixture, runID string) string {
	t.Helper()
	chunks, err := f.db.RunOutput(context.Background(), f.actor, runID, f.hosts[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, chunk := range chunks {
		text.Write(chunk.Data)
	}
	return text.String()
}

func TestSSHApprovalInteractiveInputAndTrustedReconnect(t *testing.T) {
	f := newFixture(t, "sequential", true)
	server := newSSHFixture(t, nil)
	targetSSH(t, f, server, "printf 'Name? '; IFS= read -r answer; printf 'Hello %s\\n' \"$answer\"", false, nil, "")
	run := startFixture(t, f)
	key := pendingKey(t, f, run.ID)
	if server.authentications.Load() != 0 {
		t.Fatal("credentials sent before host-key approval")
	}
	if key.Fingerprint != ssh.FingerprintSHA256(server.signer.PublicKey()) || key.PreviousFingerprint != "" {
		t.Fatalf("wrong prompt: %+v", key)
	}
	if err := f.engine.Approve(context.Background(), f.actor, ApprovalRequest{RunID: run.ID, HostID: key.HostID, Fingerprint: "stale", Accept: true}); !errors.Is(err, ErrApproval) {
		t.Fatal(err)
	}
	if err := f.engine.Approve(context.Background(), f.actor, ApprovalRequest{RunID: run.ID, HostID: key.HostID, Fingerprint: key.Fingerprint, Accept: true}); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return strings.Contains(outputText(t, f, run.ID), "Name? ") })
	if err := f.engine.Input(context.Background(), f.actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "Ada\n"}); err != nil {
		r, _ := f.db.Run(context.Background(), f.actor, run.ID)
		t.Fatalf("input: %v state=%+v output=%q", err, r, outputText(t, f, run.ID))
	}
	result := finished(t, f, run.ID)
	if result.Status != "succeeded" || result.Hosts[0].ExitCode == nil || *result.Hosts[0].ExitCode != 0 {
		t.Fatalf("SSH failed: %+v output=%q", result, outputText(t, f, run.ID))
	}
	if output := outputText(t, f, run.ID); !strings.Contains(output, "Hello Ada") || strings.Contains(output, "fixture-ssh-secret") {
		t.Fatalf("output: %q", output)
	}
	run = startFixture(t, f)
	await(t, func() bool { return strings.Contains(outputText(t, f, run.ID), "Name? ") })
	keys, err := f.engine.Pending(context.Background(), f.actor, run.ID)
	if err != nil || len(keys) != 0 {
		t.Fatal("trusted key prompted again")
	}
	if err := f.engine.Input(context.Background(), f.actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "Again\n"}); err != nil {
		t.Fatal(err)
	}
	if result := finished(t, f, run.ID); result.Status != "succeeded" {
		t.Fatal(result)
	}
}

func TestSSHChangedKeyDenialAndCancelApproval(t *testing.T) {
	f := newFixture(t, "sequential", true)
	server := newSSHFixture(t, nil)
	targetSSH(t, f, server, "echo must-not-run", false, nil, "")
	other := newSSHFixture(t, nil)
	target := f.hosts[0]
	previous := string(ssh.MarshalAuthorizedKey(other.signer.PublicKey()))
	if err := f.db.ApproveHostKey(context.Background(), f.actor, target.ID, target.Address, target.Port, previous); err != nil {
		t.Fatal(err)
	}
	run := startFixture(t, f)
	key := pendingKey(t, f, run.ID)
	if key.PreviousFingerprint != ssh.FingerprintSHA256(other.signer.PublicKey()) {
		t.Fatal("changed key not reported")
	}
	if err := f.engine.Approve(context.Background(), f.actor, ApprovalRequest{RunID: run.ID, HostID: key.HostID, Fingerprint: key.Fingerprint, Accept: false}); err != nil {
		t.Fatal(err)
	}
	if result := finished(t, f, run.ID); result.Status != "failed" {
		t.Fatal(result)
	}
	if server.authentications.Load() != 0 || outputText(t, f, run.ID) != "" {
		t.Fatal("denied host authenticated or ran")
	}
	run = startFixture(t, f)
	pendingKey(t, f, run.ID)
	if err := f.engine.Cancel(context.Background(), f.actor, HostRequest{RunID: run.ID, HostID: target.ID}); err != nil {
		t.Fatal(err)
	}
	if result := finished(t, f, run.ID); result.Status != "cancelled" {
		t.Fatal(result)
	}
	if server.authentications.Load() != 0 {
		t.Fatal("cancelled pending approval authenticated")
	}
}

func TestSSHCancellationKillsChildrenAndTerminalControl(t *testing.T) {
	for _, control := range []bool{false, true} {
		t.Run(strconv.FormatBool(control), func(t *testing.T) {
			f := newFixture(t, "sequential", true)
			server := newSSHFixture(t, nil)
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			script := "sleep 120 &\nchild=$!\nprintf '%s' \"$child\" > " + shellQuote(pidFile) + "\necho READY\nwait \"$child\""
			targetSSH(t, f, server, script, false, nil, "")
			run := startFixture(t, f)
			acceptKey(t, f, run.ID)
			await(t, func() bool { return strings.Contains(outputText(t, f, run.ID), "READY") })
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				t.Fatal(err)
			}
			// Cleanup in case of a regression, without leaving a fixture process.
			defer syscall.Kill(pid, syscall.SIGKILL)
			if control {
				err = f.engine.Input(context.Background(), f.actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "\x03"})
			} else {
				err = f.engine.Cancel(context.Background(), f.actor, HostRequest{RunID: run.ID, HostID: f.hosts[0].ID})
			}
			if err != nil {
				t.Fatal(err)
			}
			result := finished(t, f, run.ID)
			expected := "cancelled"
			if control {
				expected = "interrupted"
			}
			if result.Status != expected {
				t.Fatalf("status=%s output=%q", result.Status, outputText(t, f, run.ID))
			}
			await(t, func() bool {
				err := syscall.Kill(pid, 0)
				if errors.Is(err, syscall.ESRCH) {
					return true
				}
				stat, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				return strings.Contains(string(stat), ") Z ")
			})
		})
	}
}

func TestSSHCtrlCStopsContinuousOutput(t *testing.T) {
	f := newFixture(t, "sequential", true)
	server := newSSHFixture(t, nil)
	targetSSH(t, f, server, "yes", false, nil, "")
	run := startFixture(t, f)
	acceptKey(t, f, run.ID)
	await(t, func() bool { return strings.Contains(outputText(t, f, run.ID), "y") })
	if err := f.engine.Input(context.Background(), f.actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "\x03"}); err != nil {
		t.Fatal(err)
	}
	result := finished(t, f, run.ID)
	if result.Status != "interrupted" || result.Hosts[0].Status != "interrupted" || result.Hosts[0].FinishedAt == 0 || result.Hosts[0].ExitCode == nil || *result.Hosts[0].ExitCode != 130 {
		t.Fatalf("Ctrl+C did not interrupt yes: %+v", result)
	}
	if result.Hosts[0].Message != "Script interrupted (Ctrl+C)" {
		t.Fatal(result.Hosts[0].Message)
	}
	before := outputText(t, f, run.ID)
	time.Sleep(50 * time.Millisecond)
	if outputText(t, f, run.ID) != before {
		t.Fatal("output continued after completion")
	}
}

func TestSSHEncryptedPrivateKeyAuthentication(t *testing.T) {
	f := newFixture(t, "sequential", true)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	server := newSSHFixture(t, sshPublic)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "fixture", []byte("key-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := credentials.New(context.Background(), f.db, f.vault)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := service.Create(context.Background(), f.actor, credentials.CreateInput{Name: "SSH test key", Mode: "import", PrivateKey: string(pem.EncodeToMemory(block)), Passphrase: "key-passphrase"})
	if err != nil {
		t.Fatal(err)
	}
	targetSSH(t, f, server, "echo KEY_OK", false, nil, credential.ID)
	run := startFixture(t, f)
	acceptKey(t, f, run.ID)
	if result := finished(t, f, run.ID); result.Status != "succeeded" {
		t.Fatalf("key authentication failed: %+v output=%q", result, outputText(t, f, run.ID))
	}
	if !strings.Contains(outputText(t, f, run.ID), "KEY_OK") {
		t.Fatal("key script did not run")
	}
}

func TestSSHSudoPasswordTransportAndRootBypass(t *testing.T) {
	for _, mode := range []string{
		"password", "passwordless", "nopasswd-with-secret",
		"pty-password", "pty-passwordless", "pty-nopasswd-with-secret",
		"root", "host-disabled", "job-disabled",
	} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, "sequential", true)
			server := newSSHFixture(t, nil)
			directory := t.TempDir()
			fakeSudo := `#!/bin/bash
set -eu
printf '%s\n' call >> "$PIPED_TEST_SUDO_LOG"
if [ "$1" = '-n' ]; then
    shift
elif [ "$1" = '-S' ]; then
    shift
    [ "$1" = '-p' ]; shift 2
    if [[ "$PIPED_TEST_SUDO_MODE" = password || "$PIPED_TEST_SUDO_MODE" = pty-password ]]; then
        IFS= read -r password
        [ "$password" = 'fixture-sudo-secret' ] || exit 1
    fi
else
    exit 1
fi
[ "$1" = '--' ]; shift
case "$PIPED_TEST_SUDO_MODE" in
    root|host-disabled|job-disabled) exit 1 ;;
esac
export PIPED_TEST_ELEVATED=yes
case "$PIPED_TEST_SUDO_MODE" in
    pty-*)
        # Keep a parent in the terminal session, as sudo's PTY monitor does.
        # The supervisor must restore its foreground group after the script.
        set -m
        "$@" &
        fg %1 >/dev/null
        exit $?
        ;;
esac
exec "$@"
`
			if err := os.WriteFile(filepath.Join(directory, "sudo"), []byte(fakeSudo), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, ".bashrc"), []byte("echo UNSAFE_STARTUP\nexit 99\n"), 0600); err != nil {
				t.Fatal(err)
			}
			environment := []string{}
			for _, value := range os.Environ() {
				if !strings.HasPrefix(value, "PATH=") && !strings.HasPrefix(value, "HOME=") {
					environment = append(environment, value)
				}
			}
			environment = append(environment, "HOME="+directory, "PATH="+directory+":"+os.Getenv("PATH"), "PIPED_TEST_SUDO_MODE="+mode, "PIPED_TEST_SUDO_LOG="+filepath.Join(directory, "sudo.log"))
			server.mu.Lock()
			server.environment = environment
			server.mu.Unlock()
			var password *string
			if mode != "passwordless" && mode != "pty-passwordless" && mode != "host-disabled" {
				value := "fixture-sudo-secret"
				password = &value
			}
			script := `printf 'ENV:%s\n' "$API_TOKEN"
sh -c '[ -n "$API_TOKEN" ] && printf "CHILD_ENV_OK "'
sudo printf 'SUDO_READY ' 
command sudo -n printf 'COMMAND_OK '
sh -c 'sudo printf "SH_OK "'
bash -c 'sudo -u root printf "BASH_OK "'
printf 'Elevated:%s ' "${PIPED_TEST_ELEVATED:-no}"
if [ "$EUID" -ne 0 ]; then
    while read -r key value rest; do
        if [ "$key" = NoNewPrivs: ]; then printf 'NoNewPrivs:%s ' "$value"; fi
    done </proc/self/status
fi
IFS= read -r answer
printf 'Answer:%s\n' "$answer"`
			targetSSH(t, f, server, script, mode != "host-disabled", password, "")
			fixtureValue := "fixture-env-sensitive-token"
			setJobSecrets(t, f, []jobs.SecretInput{{Name: "API_TOKEN", Value: &fixtureValue}})
			if mode == "host-disabled" || mode == "job-disabled" {
				var err error
				f.job, err = jobService(t, f.db, f.vault).Save(context.Background(), f.actor, f.job.ID, jobs.Input{
					Name: f.job.Name, Script: f.job.Script, HostIDs: []string{f.hosts[0].ID},
					ExecutionMode: "sequential", RunAsSudo: mode != "job-disabled", Version: f.job.Version,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "root" {
				service, err := hosts.New(context.Background(), f.db, f.vault)
				if err != nil {
					t.Fatal(err)
				}
				target := f.hosts[0]
				updated, err := service.Save(context.Background(), f.actor, target.ID, hosts.Input{Name: target.Name, Address: target.Address, Port: target.Port, Username: "root", RunAsSudo: true, Version: target.Version})
				if err != nil {
					t.Fatal(err)
				}
				f.hosts[0] = updated
			}
			run := startFixture(t, f)
			acceptKey(t, f, run.ID)
			await(t, func() bool { return strings.Contains(outputText(t, f, run.ID), "SUDO_READY") })
			if strings.Contains(outputText(t, f, run.ID), "Answer:") {
				t.Fatal("sudo secret entered script stdin")
			}
			if err := f.engine.Input(context.Background(), f.actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "human-input\n"}); err != nil {
				t.Fatal(err)
			}
			result := finished(t, f, run.ID)
			output := outputText(t, f, run.ID)
			if result.Status != "succeeded" || !strings.Contains(output, "Answer:human-input") {
				t.Fatalf("sudo execution failed: %+v output=%q", result, output)
			}
			if strings.Contains(output, "[1]") || strings.Contains(output, "UNSAFE_STARTUP") {
				t.Fatalf("supervisor emitted shell startup or job notifications: %q", output)
			}
			expectedElevation := "yes"
			if mode == "root" || mode == "host-disabled" || mode == "job-disabled" {
				expectedElevation = "no"
			}
			for _, value := range []string{"ENV:[REDACTED]", "CHILD_ENV_OK", "COMMAND_OK", "SH_OK", "BASH_OK", "Elevated:" + expectedElevation} {
				if !strings.Contains(output, value) {
					t.Fatalf("missing %q in %q", value, output)
				}
			}
			if strings.Contains(output, "fixture-sudo-secret") || strings.Contains(output, "fixture-ssh-secret") || strings.Contains(output, fixtureValue) {
				t.Fatal("secret leaked to terminal")
			}
			if os.Geteuid() != 0 && !strings.Contains(output, "NoNewPrivs:1") {
				t.Fatalf("non-root execution may acquire privileges: %q", output)
			}
			cancelledRun := startFixture(t, f)
			await(t, func() bool { return strings.Contains(outputText(t, f, cancelledRun.ID), "SUDO_READY") })
			if err := f.engine.Cancel(context.Background(), f.actor, HostRequest{RunID: cancelledRun.ID, HostID: f.hosts[0].ID}); err != nil {
				t.Fatal(err)
			}
			if result := finished(t, f, cancelledRun.ID); result.Status != "cancelled" {
				t.Fatalf("cancellation failed: %+v", result)
			}
			calls, err := os.ReadFile(filepath.Join(directory, "sudo.log"))
			if expectedElevation == "no" {
				if err == nil && len(calls) > 0 {
					t.Fatal("system sudo used despite disabled policy or root connection")
				}
			} else if err != nil || strings.Count(string(calls), "call") != 3 {
				t.Fatalf("expected sudo for two starts and one cleanup, got %q: %v", calls, err)
			}
		})
	}
}

func TestSSHDisconnectAndNormalExitCleanBackgroundChildren(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		name := "normal-exit"
		if disconnect {
			name = "disconnect"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "sequential", true)
			server := newSSHFixture(t, nil)
			pidFile := filepath.Join(t.TempDir(), "background.pid")
			script := "sleep 120 &\nprintf '%s' \"$!\" > " + shellQuote(pidFile) + "\necho READY\n"
			if disconnect {
				script += "wait"
			}
			targetSSH(t, f, server, script, false, nil, "")
			run := startFixture(t, f)
			acceptKey(t, f, run.ID)
			await(t, func() bool { return strings.Contains(outputText(t, f, run.ID), "READY") })
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.Kill(pid, syscall.SIGKILL)
			if disconnect {
				server.mu.Lock()
				for _, connection := range server.conns {
					connection.Close()
				}
				server.mu.Unlock()
			}
			finished(t, f, run.ID)
			await(t, func() bool {
				if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
					return true
				}
				stat, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				return strings.Contains(string(stat), ") Z ")
			})
		})
	}
}

func setJobSecrets(t *testing.T, f *engineFixture, input []jobs.SecretInput) {
	t.Helper()
	job, err := jobService(t, f.db, f.vault).Save(context.Background(), f.actor, f.job.ID, jobs.Input{
		Name: f.job.Name, Script: f.job.Script, HostIDs: []string{f.hosts[0].ID}, ExecutionMode: f.job.ExecutionMode,
		RunAsSudo: f.job.RunAsSudo, StopOnFailure: f.job.StopOnFailure, Version: f.job.Version, Secrets: input,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.job = job
}

func TestSSHSecretTransportPreservesArbitraryTextAndTerminalInput(t *testing.T) {
	f := newFixture(t, "sequential", true)
	server := newSSHFixture(t, nil)
	targetSSH(t, f, server, `printf 'DIGEST:'
printf '%s' "$API_TOKEN" | sha256sum
printf 'VALUE:%s' "$API_TOKEN"
printf 'INPUT? '
IFS= read -r answer
printf 'ANSWER:%s\n' "$answer"`, false, nil, "")
	value := "  quoted '\" $(do-not-run) `literal` " + strings.Repeat("z", 3000) + "\r\n\x03\x04\x11\x13\x7f\u65e5\u672c\n\n"
	setJobSecrets(t, f, []jobs.SecretInput{{Name: "API_TOKEN", Value: &value}})
	t.Cleanup(func() {
		if t.Failed() {
			state, _ := f.db.Runs(context.Background(), f.actor, f.job.ID)
			if len(state) > 0 {
				data := outputText(t, f, state[0].ID)
				t.Logf("state=%+v output bytes=%d head=%q tail=%q", state[0], len(data), data[:min(120, len(data))], data[max(0, len(data)-120):])
			}
		}
	})
	run := startFixture(t, f)
	acceptKey(t, f, run.ID)
	await(t, func() bool {
		state, err := f.db.Run(context.Background(), f.actor, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(outputText(t, f, run.ID), "INPUT?") || state.FinishedAt > 0
	})
	state, _ := f.db.Run(context.Background(), f.actor, run.ID)
	if state.FinishedAt > 0 {
		t.Fatalf("execution ended before prompt: %+v, output=%q", state, outputText(t, f, run.ID))
	}
	if err := f.engine.Input(context.Background(), f.actor, InputRequest{RunID: run.ID, HostID: f.hosts[0].ID, Data: "human-input\n"}); err != nil {
		t.Fatal(err)
	}
	result := finished(t, f, run.ID)
	output := outputText(t, f, run.ID)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
	if result.Status != "succeeded" || !strings.Contains(output, "DIGEST:"+digest) || !strings.Contains(output, "VALUE:[REDACTED]") || !strings.Contains(output, "ANSWER:human-input") {
		t.Fatalf("transport or input failed: %+v output=%q", result, output)
	}
	if strings.Contains(output, "do-not-run") || strings.Contains(output, strings.Repeat("z", 100)) {
		t.Fatal("secret persisted in output")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	for _, command := range server.commands {
		if strings.Contains(command, "do-not-run") || strings.Contains(command, strings.Repeat("z", 100)) {
			t.Fatal("secret included in remote command arguments")
		}
	}
}

func TestSSHMaximumSecretEnvironment(t *testing.T) {
	f := newFixture(t, "sequential", true)
	server := newSSHFixture(t, nil)
	targetSSH(t, f, server, `count=0
for name in ${!APP_SECRET_@}; do
    value=${!name}
    [ "${#value}" -eq 4096 ] || exit 1
    count=$((count+1))
done
[ "$count" -eq 16 ] || exit 1
printf 'ALL_ENV_OK\n'`, false, nil, "")
	inputs := []jobs.SecretInput{}
	for index := 0; index < 16; index++ {
		value := fmt.Sprintf("%02d", index) + strings.Repeat("v", 4094)
		inputs = append(inputs, jobs.SecretInput{Name: fmt.Sprintf("APP_SECRET_%02d", index), Value: &value})
	}
	setJobSecrets(t, f, inputs)
	run := startFixture(t, f)
	acceptKey(t, f, run.ID)
	result := finished(t, f, run.ID)
	if result.Status != "succeeded" || !strings.Contains(outputText(t, f, run.ID), "ALL_ENV_OK") {
		t.Fatalf("maximum environment failed: %+v output=%q", result, outputText(t, f, run.ID))
	}
}

func TestScheduledSSHWaitsForApprovalAndSkipsOverlap(t *testing.T) {
	f, clock, at := scheduledFixture(t, "* * * * *")
	server := newSSHFixture(t, nil)
	targetSSH(t, f, server, "echo scheduled-approved", false, nil, "")
	f.engine.runSchedules(at)
	runs := schedulerRuns(t, f)
	if len(runs) != 1 {
		t.Fatal("schedule not admitted")
	}
	run := runs[0]
	key := pendingKey(t, f, run.ID)
	if server.authentications.Load() != 0 {
		t.Fatal("scheduled execution sent credentials before approval")
	}
	clock.Store(at.Add(time.Minute).Unix())
	f.engine.runSchedules(at.Add(time.Minute))
	if len(schedulerRuns(t, f)) != 1 {
		t.Fatal("overlapped approval wait")
	}
	if err := f.engine.Approve(context.Background(), f.actor, ApprovalRequest{RunID: run.ID, HostID: key.HostID, Fingerprint: key.Fingerprint, Accept: true}); err != nil {
		t.Fatal(err)
	}
	if result := finished(t, f, run.ID); result.Status != "succeeded" {
		t.Fatalf("scheduled SSH failed: %+v", result)
	}
}
