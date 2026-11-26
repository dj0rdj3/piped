package testdatabase

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
)

// StallingProxy holds server replies without disconnecting TCP. An empty
// statement stalls every connection; otherwise exactly one connection whose
// request contains the statement is selected after stalling is enabled. Targeted
// matching requires the disposable fixture's unencrypted protocol.
func StallingProxy(t testing.TB, driver, dsn, statement string) (string, func(), func(), <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var destination string
	var proxyDSN string
	if driver == "postgres" {
		parsed, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal("parse disposable PostgreSQL settings")
		}
		destination = net.JoinHostPort(parsed.Host, strconv.Itoa(int(parsed.Port)))
		uri, err := url.Parse(dsn)
		if err == nil && (uri.Scheme == "postgres" || uri.Scheme == "postgresql") {
			uri.Host = listener.Addr().String()
			if statement != "" {
				// Repeat the SQL text so targeted faults also match cached queries.
				query := uri.Query()
				query.Set("default_query_exec_mode", "simple_protocol")
				uri.RawQuery = query.Encode()
			}
			proxyDSN = uri.String()
		} else {
			host, port, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			proxyDSN = dsn + " host=" + host + " port=" + port
			if statement != "" {
				proxyDSN += " default_query_exec_mode=simple_protocol"
			}
		}
	} else {
		parsed, err := mysql.ParseDSN(dsn)
		if err != nil || parsed.Net != "tcp" {
			t.Fatal("requires disposable TCP MySQL/MariaDB settings")
		}
		destination = parsed.Addr
		parsed.Addr = listener.Addr().String()
		proxyDSN = parsed.FormatDSN()
	}
	var stalled atomic.Bool
	var selected atomic.Bool
	release := make(chan struct{})
	blocked := make(chan struct{}, 1)
	var once sync.Once
	resume := func() { once.Do(func() { close(release) }) }
	var mu sync.Mutex
	done := make(chan struct{})
	var connections []net.Conn
	var workers sync.WaitGroup
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
			select {
			case <-done:
				mu.Unlock()
				client.Close()
				server.Close()
				return
			default:
			}
			connections = append(connections, client, server)
			mu.Unlock()
			var hold atomic.Bool
			workers.Add(2)
			go func() {
				defer workers.Done()
				defer client.Close()
				defer server.Close()
				buffer := make([]byte, 32*1024)
				var tail string
				for {
					count, err := client.Read(buffer)
					if count > 0 {
						if statement != "" {
							window := tail + string(buffer[:count])
							if stalled.Load() && strings.Contains(window, statement) && selected.CompareAndSwap(false, true) {
								hold.Store(true)
							}
							tail = window[max(0, len(window)-len(statement)+1):]
						}
						if _, err := server.Write(buffer[:count]); err != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
			go func() {
				defer workers.Done()
				defer client.Close()
				defer server.Close()
				buffer := make([]byte, 32*1024)
				for {
					count, err := server.Read(buffer)
					if count > 0 {
						if stalled.Load() && (statement == "" || hold.Load()) {
							select {
							case blocked <- struct{}{}:
							default:
							}
							<-release
						}
						if _, err := client.Write(buffer[:count]); err != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		resume()
		close(done)
		listener.Close()
		mu.Lock()
		for _, conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return proxyDSN, func() { stalled.Store(true) }, resume, blocked
}
