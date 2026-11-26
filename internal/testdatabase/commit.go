package testdatabase

import (
	"encoding/binary"
	"fmt"
	"io"
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

// DropAdmissionCommit drops exactly one admission COMMIT, either before it reaches the disposable
// server or after the server confirms success. Other traffic remains intact.
func DropAdmissionCommit(t testing.TB, driver, dsn string, afterCommit bool) (string, <-chan struct{}) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Register immediately so invalid settings cannot leave a listening socket.
	t.Cleanup(func() { listener.Close() })
	destination, proxyDSN := "", ""
	postgres := driver == "postgres"
	if postgres {
		parsed, err := pgx.ParseConfig(dsn)
		if err != nil || parsed.TLSConfig != nil {
			t.Fatal("admission fault requires disposable unencrypted TCP PostgreSQL")
		}
		destination = net.JoinHostPort(parsed.Host, strconv.Itoa(int(parsed.Port)))
		uri, err := url.Parse(dsn)
		if err == nil && (uri.Scheme == "postgres" || uri.Scheme == "postgresql") {
			uri.Host = listener.Addr().String()
			query := uri.Query()
			query.Set("default_query_exec_mode", "simple_protocol")
			uri.RawQuery = query.Encode()
			proxyDSN = uri.String()
		} else {
			host, port, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			proxyDSN = dsn + " host=" + host + " port=" + port + " default_query_exec_mode=simple_protocol"
		}
	} else {
		parsed, err := mysql.ParseDSN(dsn)
		if err != nil || parsed.Net != "tcp" || parsed.TLSConfig != "" {
			t.Fatal("admission fault requires disposable unencrypted TCP MySQL/MariaDB")
		}
		destination = parsed.Addr
		parsed.Addr = listener.Addr().String()
		proxyDSN = parsed.FormatDSN()
	}
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
				first := postgres
				for {
					packet, err := commitPacket(client, postgres, first)
					first = false
					if err != nil {
						return
					}
					isAdmission, isCommit := commitStatement(packet, postgres)
					if isAdmission {
						admission = true
					}
					if admission && isCommit && injected.CompareAndSwap(false, true) {
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
					packet, err := commitPacket(server, postgres, false)
					if err != nil {
						return
					}
					if awaitingCommit.Load() && (!postgres || packet[0] == 'C' || packet[0] == 'E') {
						if !commitAcknowledged(packet, postgres) {
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
	return proxyDSN, dropped
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

func commitStatement(packet []byte, postgres bool) (bool, bool) {
	if postgres {
		if len(packet) < 6 || packet[0] != 'Q' {
			return false, false
		}
		statement := strings.TrimSuffix(string(packet[5:]), "\x00")
		return strings.Contains(strings.ToUpper(statement), "INSERT INTO RUNS"), strings.EqualFold(statement, "COMMIT")
	}
	payload := packet[4:]
	if len(payload) < 2 {
		return false, false
	}
	return payload[0] == 0x16 && strings.Contains(strings.ToUpper(string(payload[1:])), "INSERT INTO RUNS"), payload[0] == 0x03 && strings.EqualFold(string(payload[1:]), "COMMIT")
}

func commitAcknowledged(packet []byte, postgres bool) bool {
	if postgres {
		return packet[0] == 'C' && string(packet[5:]) == "COMMIT\x00"
	}
	return len(packet) >= 5 && packet[4] == 0x00
}

func commitPacket(connection net.Conn, postgres, startup bool) ([]byte, error) {
	if !postgres {
		return mysqlPacket(connection)
	}
	headerSize := 5
	if startup {
		headerSize = 4
	}
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint32(header[headerSize-4:]))
	if size < 4 || size > 16*1024*1024 {
		return nil, fmt.Errorf("invalid disposable PostgreSQL frame size")
	}
	packet := make([]byte, headerSize+size-4)
	copy(packet, header)
	_, err := io.ReadFull(connection, packet[headerSize:])
	return packet, err
}
