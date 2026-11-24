//go:build linux

package execution

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Opt-in bridge for browser checks. Reuse the real PTY fixture without adding
// a server feature or exposing test infrastructure in either shipped binary.
func TestBrowserSSHFixture(t *testing.T) {
	path := os.Getenv("PIPED_BROWSER_SSH_FIXTURE")
	if path == "" {
		t.Skip("set PIPED_BROWSER_SSH_FIXTURE to a new metadata file for browser checks")
	}
	server := newSSHFixture(t, nil)
	_, port, err := net.SplitHostPort(server.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Remove(path)
		os.Remove(path + ".stop")
	})
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		file.Close()
		t.Fatal(err)
	}
	err = json.NewEncoder(file).Encode(struct {
		Port        int    `json:"port"`
		Fingerprint string `json:"fingerprint"`
	}{portNumber, ssh.FingerprintSHA256(server.signer.PublicKey())})
	closeErr := file.Close()
	if err := errors.Join(err, closeErr); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser SSH fixture watchdog expired")
		case <-ticker.C:
			if _, err := os.Stat(path + ".stop"); err == nil {
				return
			}
		}
	}
}
