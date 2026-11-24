package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

type terminalPrompter struct {
	ctx    context.Context
	file   *os.File
	output io.Writer
}

func newTerminalPrompter(ctx context.Context, input io.Reader, output io.Writer) (prompter, error) {
	file, ok := input.(*os.File)
	if !ok {
		return nil, errors.New("register requires an interactive terminal")
	}
	if _, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS); err != nil {
		return nil, errors.New("register requires an interactive terminal; development automation can use piped-dev init-admin --password-stdin")
	}
	return &terminalPrompter{ctx: ctx, file: file, output: output}, nil
}

func (p *terminalPrompter) ReadLine(prompt string, secret bool) (value string, err error) {
	fd := int(p.file.Fd())
	if secret {
		original, err := unix.IoctlGetTermios(fd, unix.TCGETS)
		if err != nil {
			return "", fmt.Errorf("read terminal settings: %w", err)
		}
		hidden := *original
		hidden.Lflag &^= unix.ECHO | unix.ECHONL
		hidden.Lflag |= unix.ICANON | unix.ISIG
		hidden.Iflag |= unix.ICRNL
		if err := unix.IoctlSetTermios(fd, unix.TCSETS, &hidden); err != nil {
			return "", fmt.Errorf("disable password echo: %w", err)
		}
		defer func() {
			restoreErr := unix.IoctlSetTermios(fd, unix.TCSETS, original)
			_, newlineErr := fmt.Fprintln(p.output)
			if restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore terminal settings: %w", restoreErr))
			}
			err = errors.Join(err, newlineErr)
		}()
	}
	if _, err := fmt.Fprint(p.output, prompt); err != nil {
		return "", err
	}
	line := make([]byte, 0, 1024)
	defer func() { clear(line) }()
	for {
		if err := p.ctx.Err(); err != nil {
			return "", err
		}
		// Poll rather than block indefinitely so Ctrl+C can restore terminal echo.
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		ready, err := unix.Poll(fds, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return "", err
		}
		if ready == 0 {
			continue
		}
		var b [1]byte
		n, err := unix.Read(fd, b[:])
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", io.EOF
		}
		if b[0] == '\n' {
			return string(line), nil
		}
		if len(line) == 1024 {
			return "", errors.New("input is too long")
		}
		line = append(line, b[0])
	}
}
