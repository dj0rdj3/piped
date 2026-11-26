package store

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockSQLiteExecution(filename string) (func() error, error) {
	// Linux flock is independent of SQLite's POSIX byte-range locks. Lock the
	// database inode itself so path aliases cannot create a second owner, and
	// closing a CLI connection cannot release the server's ownership.
	file, err := os.OpenFile(filename, os.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrExecutionOwned
		}
		return nil, err
	}
	return file.Close, nil
}
