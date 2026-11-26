package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"piped/internal/auth"
)

type prompter interface {
	ReadLine(prompt string, secret bool) (string, error)
}

func register(ctx context.Context, service *auth.Service, prompts prompter, output io.Writer) error {
	var username string
	for {
		value, err := prompts.ReadLine("Administrator username [admin]: ", false)
		if err != nil {
			return err
		}
		if value == "" {
			value = "admin"
		}
		username, err = auth.NormalizeUsername(value)
		if err == nil {
			break
		}
		if _, err := fmt.Fprintln(output, err); err != nil {
			return err
		}
	}

	for {
		password, err := prompts.ReadLine("Password (8-128 characters): ", true)
		if err != nil {
			return err
		}
		if err := auth.ValidatePassword(password); err != nil {
			if _, err := fmt.Fprintln(output, err); err != nil {
				return err
			}
			continue
		}
		confirmation, err := prompts.ReadLine("Confirm password: ", true)
		if err != nil {
			return err
		}
		if password != confirmation {
			if _, err := fmt.Fprintln(output, "Passwords do not match. Please try again."); err != nil {
				return err
			}
			continue
		}
		setup, cancel := context.WithTimeout(ctx, 5*time.Second)
		user, err := service.InitAdmin(setup, username, password)
		cancel()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "Created administrator:", user.Username)
		return err
	}
}
