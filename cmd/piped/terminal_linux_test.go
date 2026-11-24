package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func terminalPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	return master, slave
}

func TestHiddenTerminalInputAndRestoration(t *testing.T) {
	for _, action := range []string{"success", "cancel", "eof", "too-long"} {
		t.Run(action, func(t *testing.T) {
			master, slave := terminalPair(t)
			before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			readOutput, writeOutput, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer readOutput.Close()
			defer writeOutput.Close()
			prompts, err := newTerminalPrompter(ctx, slave, writeOutput)
			if err != nil {
				t.Fatal(err)
			}
			type result struct {
				value string
				err   error
			}
			done := make(chan result, 1)
			go func() { value, err := prompts.ReadLine("Password: ", true); done <- result{value, err} }()
			prompt := make([]byte, len("Password: "))
			if _, err := io.ReadFull(readOutput, prompt); err != nil {
				t.Fatal(err)
			}
			hidden, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			if hidden.Lflag&(unix.ECHO|unix.ECHONL) != 0 {
				t.Fatal("password input is echoed")
			}
			password := "  a private password with spaces  "
			switch action {
			case "success":
				_, err = io.WriteString(master, password+"\n")
			case "cancel":
				cancel()
			case "eof":
				_, err = master.Write([]byte{4})
			case "too-long":
				_, err = io.WriteString(master, strings.Repeat("x", 1025)+"\n")
			}
			if err != nil {
				t.Fatal(err)
			}
			var got result
			select {
			case got = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("terminal read did not finish")
			}
			switch action {
			case "success":
				if got.err != nil || got.value != password {
					t.Fatalf("password read failed: %v", got.err)
				}
			case "cancel":
				if !errors.Is(got.err, context.Canceled) {
					t.Fatalf("cancel: %v", got.err)
				}
			case "eof":
				if !errors.Is(got.err, io.EOF) {
					t.Fatalf("EOF: %v", got.err)
				}
			case "too-long":
				if got.err == nil {
					t.Fatal("oversized input accepted")
				}
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			if *before != *after {
				t.Fatal("terminal settings were not restored")
			}
		})
	}
}

func TestRegisterRequiresTerminal(t *testing.T) {
	if _, err := newTerminalPrompter(context.Background(), strings.NewReader("secret"), &bytes.Buffer{}); err == nil {
		t.Fatal("accepted piped input")
	}
	f, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := newTerminalPrompter(context.Background(), f, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted regular file input")
	}
}
