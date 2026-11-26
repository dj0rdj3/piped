package store

import (
	"errors"
	"testing"
)

type failingRunRows struct {
	reads   int
	closed  bool
	failure error
}

func (r *failingRunRows) Next() bool               { r.reads++; return r.reads == 1 }
func (r *failingRunRows) Scan(values ...any) error { *values[0].(*string) = "first-host"; return nil }
func (r *failingRunRows) Err() error               { return r.failure }
func (r *failingRunRows) Close() error             { r.closed = true; return nil }

func TestRunHostStreamFailureDiscardsFirstSuccessfulRow(t *testing.T) {
	failure := errors.New("stream lost after first host")
	rows := &failingRunRows{failure: failure}
	hosts, err := readRunHosts(rows)
	if !errors.Is(err, failure) || hosts != nil || rows.reads != 2 || !rows.closed {
		t.Fatal("partial replay rows escaped a failed read", hosts, err)
	}
}
