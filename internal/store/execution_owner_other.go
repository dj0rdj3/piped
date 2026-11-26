//go:build !linux

package store

import "errors"

func lockSQLiteExecution(string) (func() error, error) {
	return nil, errors.New("SQLite execution ownership requires Linux")
}
