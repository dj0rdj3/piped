package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"piped/internal/secrets"
)

type failingListener struct{ err error }

func (l failingListener) Accept() (net.Conn, error) { return nil, l.err }
func (failingListener) Close() error                { return nil }
func (failingListener) Addr() net.Addr              { return &net.TCPAddr{} }

func TestServeFailureCancelsLifecycleAndReturnsOriginalError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failure := errors.New("fixture accept failure")
	server := &http.Server{ReadHeaderTimeout: time.Second}
	err := serveHTTP(ctx, cancel, server, failingListener{failure}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, failure) || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("serve failure did not cancel the lifecycle: %v, %v", err, ctx.Err())
	}
}

func TestServeAlreadyCancelledClosesListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	server := &http.Server{ReadHeaderTimeout: time.Second}
	if err := serveHTTP(ctx, cancel, server, listener, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err == nil {
		connection.Close()
		t.Fatal("cancelled startup left its listener open")
	}
}

func TestGracefulShutdownDrainsActiveRequestBeforeReturning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	releaseRequest := sync.OnceFunc(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	server := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			select {
			case <-release:
				_, _ = io.WriteString(w, "drained")
			case <-r.Context().Done():
				t.Error("graceful shutdown cancelled an active request")
			}
		}),
	}
	finished := make(chan struct{})
	var serveErr error
	go func() {
		defer close(finished)
		serveErr = serveHTTP(ctx, cancel, server, listener, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	defer func() {
		releaseRequest()
		cancel()
		server.Close()
		<-finished
	}()
	clientResult := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Get("http://" + listener.Addr().String())
		if err != nil {
			clientResult <- err
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err == nil && string(body) != "drained" {
			err = errors.New("active response was not drained")
		}
		clientResult <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case <-finished:
		t.Fatal("shutdown returned while a request was still active")
	case <-time.After(20 * time.Millisecond):
	}
	releaseRequest()
	if err := <-clientResult; err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not join HTTP serving")
	}
	if serveErr != nil {
		t.Fatal(serveErr)
	}
}

type failedOutput struct{ err error }

func (w failedOutput) Write([]byte) (int, error) { return 0, w.err }

func TestKeygenOutputFailureRetainsUsablePrivateConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())
	err := run([]string{"keygen"}, strings.NewReader(""), failedOutput{io.ErrClosedPipe})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output failure was hidden: %v", err)
	}
	configuration, err := secrets.LoadConfiguration(".env")
	if err != nil || configuration.Vault == nil {
		t.Fatalf("output failure removed the generated deployment key: %v", err)
	}
	before, err := os.ReadFile(".env")
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"keygen"}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("retry replaced an existing deployment key")
	}
	after, err := os.ReadFile(".env")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("retry changed the private configuration")
	}
}

func TestShutdownTimeoutForcesConnectionClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started, handlerDone := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	server := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			defer close(handlerDone)
			close(started)
			<-r.Context().Done()
		}),
	}
	var logs bytes.Buffer
	finished := make(chan struct{})
	var serveErr error
	go func() {
		defer close(finished)
		serveErr = serveHTTP(ctx, cancel, server, listener, slog.New(slog.NewTextHandler(&logs, nil)))
	}()
	defer func() {
		cancel()
		server.Close()
		<-finished
	}()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		client := &http.Client{Timeout: 15 * time.Second}
		response, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	at := time.Now()
	cancel()
	select {
	case <-finished:
	case <-time.After(13 * time.Second):
		t.Fatal("shutdown exceeded its drain deadline")
	}
	if time.Since(at) < 9*time.Second || serveErr != nil || !strings.Contains(logs.String(), "graceful shutdown failed") {
		t.Fatal("shutdown did not apply its graceful deadline and forced-close fallback")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("forced close did not cancel the HTTP request")
	}
	<-clientDone
}
