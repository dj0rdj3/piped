package execution

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/lifecycle"
	"piped/internal/store"
)

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// The supervisor owns the SSH terminal session. Bash job control gives the
// script its own foreground process group while keeping a controlling terminal
// for prompts and terminal controls. EXIT/HUP/TERM remove the entire group,
// including background children, before the supervisor exits.
func remoteCommand(script string, sudo, password bool, marker string, secretCount int) string {
	supervisor := `command -v stty >/dev/null || exit 1
set -m
child=
guard=
runtime=
cleanup() {
    trap '' HUP TERM
    if [ -n "$guard" ]; then
        kill -KILL -- "-$guard" 2>/dev/null || :
        wait "$guard" 2>/dev/null || :
    fi
    if [ -n "$child" ]; then
        kill -KILL -- "-$child" 2>/dev/null || :
        wait "$child" 2>/dev/null || :
    fi
    if [ -n "$runtime" ]; then
        command rm -rf -- "$runtime"
    fi
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 129' HUP
runtime=$(umask 077; command mktemp -d /tmp/piped-run.XXXXXXXXXX) || exit 1
printf '%s\n' '#!/bin/bash' ` + shellQuote(managedSudo) + ` 'sudo "$@"' > "$runtime/sudo" || exit 1
command chmod 700 "$runtime/sudo" || exit 1
piped_secret_names=()
piped_secret_values=()
` + environmentBootstrap(secretCount, marker) + `
piped_script() {
    for piped_index in "${!piped_secret_names[@]}"; do
        export "${piped_secret_names[$piped_index]}=${piped_secret_values[$piped_index]}" || return 1
    done
    unset piped_secret_names piped_secret_values piped_secret_name piped_secret_value
    export PATH="$runtime:$PATH"
    ` + managedSudo + `
    export -f sudo
    if [ "$EUID" -ne 0 ]; then
        if ! command -v setpriv >/dev/null; then
            printf '%s\n' 'Piped: non-root execution requires setpriv for the configured sudo policy.' >&2
            return 1
        fi
        command setpriv --no-new-privs -- bash -c ` + shellQuote(script) + `
    else
        bash -c ` + shellQuote(script) + `
    fi
}
piped_script </dev/tty &
child=$!
unset piped_secret_names piped_secret_values piped_secret_name piped_secret_value
piped_watch_terminal() {
    trap '' HUP
    while stty -a </dev/tty >/dev/null 2>&1; do
        sleep 1
    done
    kill -KILL -- "-$child" 2>/dev/null || :
}
piped_watch_terminal &
guard=$!
printf '\036` + marker + `:%s\037' "$child"
fg %1 >/dev/null
result=$?
exit "$result"`
	command := supervisor
	if sudo {
		// Password transport is separated from script stdin. Even NOPASSWD
		// rules cannot accidentally deliver a stored password to the script.
		// Sudo can start its command in a background group when stdin is a
		// password pipe. An interactive bootstrap acquires the foreground tty
		// and initializes job-control signals before execing the noninteractive
		// supervisor. Skip startup files and avoid interactive job notifications.
		bootstrap := "trap - TERM QUIT\nexec bash -c \"$1\""
		elevated := "bash --noprofile --norc -ic " + shellQuote(bootstrap) +
			" piped-supervisor " + shellQuote("exec </dev/tty\n"+supervisor)
		command = "exec sudo -n -- " + elevated
		if password {
			command = "IFS= read -r piped_sudo_password || exit 1\nprintf '%s\\n' \"$piped_sudo_password\" | sudo -S -p '' -- " + elevated
		}
	}
	return "exec bash -c " + shellQuote(command)
}

func (e *Engine) authentication(c store.Connection) (ssh.AuthMethod, error) {
	if c.Host.CredentialID == "" {
		password, err := e.vault.Open("host:"+c.Host.ID+":password", c.Password)
		if err != nil {
			return nil, err
		}
		defer clear(password)
		return ssh.Password(string(password)), nil
	}
	plain, err := e.vault.Open("credential:"+c.Host.CredentialID+":secret", c.Credential)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	var secret struct {
		PrivateKey string `json:"private_key"`
		Passphrase string `json:"passphrase"`
	}
	if err := json.Unmarshal(plain, &secret); err != nil {
		return nil, err
	}
	defer func() { secret.PrivateKey, secret.Passphrase = "", "" }()
	var signer ssh.Signer
	if secret.Passphrase == "" {
		signer, err = ssh.ParsePrivateKey([]byte(secret.PrivateKey))
	} else {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(secret.PrivateKey), []byte(secret.Passphrase))
	}
	if err != nil {
		return nil, err
	}
	return ssh.PublicKeys(signer), nil
}

func (e *Engine) runSSH(ctx context.Context, c store.Connection, script string, output io.Writer, ready func(io.WriteCloser), verify func(ssh.PublicKey) error) (*int, error) {
	auth, err := e.authentication(c)
	if err != nil {
		return nil, err
	}
	client, connection, stop, err := e.connectSSH(ctx, c, auth, verify)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	defer client.Close()
	defer stop()
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	// Keep terminal input out of saved output, including sudo secrets and
	// answers to password prompts. TERM=dumb avoids unsupported screen modes.
	modes := ssh.TerminalModes{ssh.ECHO: 0, ssh.ICANON: 1, ssh.ISIG: 1, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400}
	if err := session.RequestPty("dumb", 30, 120, modes); err != nil {
		return nil, err
	}
	input, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer input.Close()
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	marker := "piped-" + hex.EncodeToString(random)
	processGroup := make(chan int, 1)
	environmentReady := make(chan struct{}, 1)
	processStarted := make(chan struct{})
	terminalOutput := &controlOutput{output: output, marker: "\x1e" + marker + ":", group: processGroup, ready: environmentReady, started: processStarted}
	session.Stdout, session.Stderr = terminalOutput, output
	sudo := c.RunAsSudo
	var password []byte
	if sudo && len(c.SudoPassword) > 0 {
		password, err = e.vault.Open("host:"+c.Host.ID+":sudo-password", c.SudoPassword)
		if err != nil {
			return nil, err
		}
		defer clear(password)
		if strings.ContainsAny(string(password), "\r\n") {
			return nil, errors.New("sudo password contains a line break")
		}
	}
	// Replace handshake cancellation with process-group cleanup while the SSH
	// transport is still open. A deadline bounds a nonresponsive cleanup channel.
	stop()
	cleanupDone := make(chan struct{})
	cleanupStop := context.AfterFunc(ctx, func() {
		defer close(cleanupDone)
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(lifecycle.SSHCleanupTimeout))
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case group := <-processGroup:
			cleanup, err := client.NewSession()
			if err != nil {
				return
			}
			defer cleanup.Close()
			command := fmt.Sprintf("kill -KILL -- -%d", group)
			if sudo {
				command = "sudo -n -- bash -c " + shellQuote(command)
				if len(password) > 0 {
					command = "sudo -S -p '' -- bash -c " + shellQuote(fmt.Sprintf("kill -KILL -- -%d", group))
					cleanup.Stdin = strings.NewReader(string(password) + "\n")
				}
			}
			if err := cleanup.Run(command); err != nil {
				e.logger.Warn("remote process cleanup could not be confirmed")
			}
		case <-timer.C:
		}
	})
	defer func() {
		if !cleanupStop() {
			<-cleanupDone
		}
	}()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := session.Start(remoteCommand(script, sudo, len(password) > 0, marker, len(c.Environment))); err != nil {
		return nil, err
	}
	if len(password) > 0 {
		line := append(append([]byte(nil), password...), '\n')
		defer clear(line)
		if _, err := input.Write(line); err != nil {
			return nil, err
		}
	}
	if len(c.Environment) > 0 {
		timer := time.NewTimer(lifecycle.EnvironmentTimeout)
		defer timer.Stop()
		select {
		case <-environmentReady:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, errors.New("remote environment initialization timed out")
		}
		payload := []byte{}
		for _, variable := range c.Environment {
			encoded := make([]byte, base64.StdEncoding.EncodedLen(len(variable.Value)))
			base64.StdEncoding.Encode(encoded, variable.Value)
			payload = append(payload, variable.Name...)
			payload = append(payload, '\n')
			payload = strconv.AppendInt(payload, int64((len(encoded)+1023)/1024), 10)
			payload = append(payload, '\n')
			for len(encoded) > 0 {
				size := min(len(encoded), 1024)
				payload = append(payload, encoded[:size]...)
				payload = append(payload, '\n')
				clear(encoded[:size])
				encoded = encoded[size:]
			}
		}
		defer clear(payload)
		if _, err := input.Write(payload); err != nil {
			return nil, err
		}
		select {
		case <-processStarted:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, errors.New("remote environment initialization timed out")
		}
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	ready(input)
	err = session.Wait()
	if flushErr := terminalOutput.Flush(); err == nil {
		err = flushErr
	}
	code := 0
	if err != nil {
		var exit *ssh.ExitError
		if !errors.As(err, &exit) {
			return nil, err
		}
		code = exit.ExitStatus()
	}
	return &code, err
}

// controlOutput removes the supervisor's one-time process-group record before
// persisting output. It handles arbitrary SSH packet boundaries without holding
// more than a small protocol record in memory.
type controlOutput struct {
	mu        sync.Mutex
	output    io.Writer
	marker    string
	group     chan int
	ready     chan struct{}
	started   chan struct{}
	announced bool
	pending   string
	found     bool
}

func (w *controlOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.found {
		return w.output.Write(data)
	}
	size := len(data)
	w.pending += string(data)
	for {
		start := strings.Index(w.pending, w.marker)
		if start < 0 {
			keep := min(len(w.pending), len(w.marker)-1)
			for keep > 0 && !strings.HasSuffix(w.pending, w.marker[:keep]) {
				keep--
			}
			emit := w.pending[:len(w.pending)-keep]
			if _, err := io.WriteString(w.output, emit); err != nil {
				return 0, err
			}
			w.pending = w.pending[len(w.pending)-keep:]
			return size, nil
		}
		if start > 0 {
			if _, err := io.WriteString(w.output, w.pending[:start]); err != nil {
				return 0, err
			}
			w.pending = w.pending[start:]
		}
		end := strings.IndexByte(w.pending, '\x1f')
		if end < 0 {
			if len(w.pending) > len(w.marker)+20 {
				return 0, errors.New("invalid remote process group")
			}
			return size, nil
		}
		record := w.pending[len(w.marker):end]
		if record == "ready" {
			if w.ready == nil || w.announced {
				return 0, errors.New("invalid remote environment initialization")
			}
			w.announced = true
			w.ready <- struct{}{}
			w.pending = w.pending[end+1:]
			continue
		}
		group, err := strconv.Atoi(record)
		if err != nil || group < 2 {
			return 0, errors.New("invalid remote process group")
		}
		w.group <- group
		w.found = true
		if w.started != nil {
			close(w.started)
		}
		_, err = io.WriteString(w.output, w.pending[end+1:])
		w.pending = ""
		return size, err
	}
}

func (w *controlOutput) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := io.WriteString(w.output, w.pending)
	w.pending = ""
	return err
}

// Unknown or changed keys are inspected on a connection that is closed before
// asking for approval. This avoids SSH servers' login grace periods expiring
// while the user considers the key. The authenticated reconnect pins exactly
// the approved key, and a further change starts a fresh approval request.
func (e *Engine) connectSSH(ctx context.Context, c store.Connection, auth ssh.AuthMethod, verify func(ssh.PublicKey) error) (*ssh.Client, net.Conn, func() bool, error) {
	address := net.JoinHostPort(c.Host.Address, strconv.Itoa(c.Host.Port))
	lookup, cancel := e.operationContext(ctx)
	trusted, err := e.db.TrustedHostKey(lookup, c.Host.ID, c.Host.Address, c.Host.Port)
	cancel()
	if err != nil {
		return nil, nil, nil, err
	}
	for {
		connection, err := (&net.Dialer{Timeout: lifecycle.SSHHandshakeTimeout, KeepAlive: 15 * time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, nil, nil, err
		}
		stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
		if err := connection.SetDeadline(time.Now().Add(lifecycle.SSHHandshakeTimeout)); err != nil {
			stop()
			connection.Close()
			return nil, nil, nil, err
		}
		var presented ssh.PublicKey
		configuration := &ssh.ClientConfig{User: c.Host.Username, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if string(ssh.MarshalAuthorizedKey(key)) == trusted {
				return nil
			}
			presented = key
			return errors.New("SSH key requires approval")
		}}
		transport, channels, requests, err := ssh.NewClientConn(connection, address, configuration)
		if err == nil {
			return ssh.NewClient(transport, channels, requests), connection, stop, nil
		}
		stop()
		connection.Close()
		if presented == nil {
			return nil, nil, nil, err
		}
		if err := verify(presented); err != nil {
			return nil, nil, nil, err
		}
		trusted = string(ssh.MarshalAuthorizedKey(presented))
	}
}
