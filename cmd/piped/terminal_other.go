//go:build !linux

package main

import (
	"context"
	"errors"
	"io"
)

func newTerminalPrompter(context.Context, io.Reader, io.Writer) (prompter, error) {
	return nil, errors.New("interactive registration currently supports Linux; development automation can use piped-dev init-admin --password-stdin")
}
