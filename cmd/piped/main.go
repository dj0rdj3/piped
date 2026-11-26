package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"piped/internal/auth"
	"piped/internal/credentials"
	"piped/internal/execution"
	"piped/internal/hosts"
	"piped/internal/httpserver"
	"piped/internal/jobs"
	"piped/internal/notifications"
	"piped/internal/secrets"
	"piped/internal/store"
	"piped/web"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "piped:", err)
		os.Exit(1)
	}
}

func run(args []string, input io.Reader, output io.Writer) error {
	options, err := parseOptions(args, output)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if options.command == "keygen" {
		if err := secrets.GenerateEnv(options.envFile); err != nil {
			return err
		}
		_, err := fmt.Fprintln(output, "Created private .env configuration. Back it up separately from the database.")
		return err
	}
	if options.command == "init-admin" && !options.passwordStdin {
		return errors.New("use --password-stdin; passwords must not be command-line arguments")
	}
	configuration, err := loadConfiguration(options)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()
	db, err := store.OpenDatabase(ctx, store.Config{
		Driver: configuration.DatabaseDriver, Path: configuration.DatabasePath, DSN: configuration.DatabaseDSN,
	})
	if err != nil {
		return err
	}
	defer db.Close()
	// The build constant also removes key issuance from the production binary;
	// rejecting the command in parseOptions alone would leave this code linked.
	if httpserver.DevelopmentBuild && options.command == "api-key" {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if options.keyRevoke != "" {
			decoded, err := hex.DecodeString(options.keyRevoke)
			if err != nil || len(decoded) != 16 {
				return errors.New("invalid API key ID")
			}
			if err := db.RevokeLocalAPIKey(ctx, options.keyRevoke); err != nil {
				return err
			}
			if options.keyJSON {
				return json.NewEncoder(output).Encode(map[string]bool{"revoked": true})
			}
			_, err = fmt.Fprintln(output, "Revoked development API key")
			return err
		}
		name, err := auth.NormalizeUsername(options.username)
		if err != nil {
			return err
		}
		user, err := db.UserByName(ctx, name)
		if err != nil {
			return err
		}
		key, token, err := auth.NewAPIKey(user, options.keyName, strings.Split(options.keyScopes, ","), options.keyLifetime)
		if err != nil {
			return err
		}
		if err := db.CreateLocalAPIKey(ctx, user, key, auth.TokenHash(token)); err != nil {
			return err
		}
		if options.keyJSON {
			err = json.NewEncoder(output).Encode(struct {
				Key   store.APIKey `json:"key"`
				Token string       `json:"token"`
			}{Key: key, Token: token})
		} else {
			_, err = fmt.Fprintln(output, token)
		}
		if err != nil {
			// Clean up keys whose token could not be written.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = errors.Join(err, db.RevokeLocalAPIKey(cleanupCtx, key.ID))
		}
		return err
	}
	if options.command == "register" {
		lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
		initialized, err := db.HasUsers(lookup)
		cancel()
		if err != nil {
			return err
		}
		if initialized {
			return store.ErrInitialized
		}
		prompts, err := newTerminalPrompter(ctx, input, output)
		if err != nil {
			return err
		}
		a, err := auth.New(db)
		if err != nil {
			return err
		}
		return register(ctx, a, prompts, output)
	}
	a, err := auth.New(db)
	if err != nil {
		return err
	}
	if httpserver.DevelopmentBuild && options.command == "init-admin" {
		password, err := io.ReadAll(io.LimitReader(input, 1025))
		if err != nil {
			return err
		}
		defer clear(password)
		if len(password) > 1024 {
			return errors.New("password input too long")
		}
		secret := strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r")
		setup, cancel := context.WithTimeout(ctx, 5*time.Second)
		u, err := a.InitAdmin(setup, options.username, secret)
		cancel()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "Created administrator:", u.Username)
		return err
	}
	assets, err := web.Assets()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	setup, cancelSetup := context.WithTimeout(ctx, 15*time.Second)
	defer cancelSetup()
	credentialService, err := credentials.New(setup, db, configuration.Vault)
	if err != nil {
		return err
	}
	hostService, err := hosts.New(setup, db, configuration.Vault)
	if err != nil {
		return err
	}
	jobService, err := jobs.New(setup, db, configuration.Vault)
	if err != nil {
		return err
	}
	notificationService, err := notifications.New(setup, db, configuration.Vault)
	if err != nil {
		return err
	}
	cancelSetup()
	listener, err := net.Listen("tcp", configuration.ListenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	engine, err := execution.New(ctx, db, configuration.Vault, logger, configuration.LogRetentionDays)
	if err != nil {
		return err
	}
	defer engine.Close()
	// Ownership loss stops HTTP admission as well as the execution workers.
	engineStopped := make(chan struct{})
	go func() {
		defer close(engineStopped)
		select {
		case <-engine.Done():
			stop()
		case <-ctx.Done():
		}
	}()
	defer func() {
		stop()
		<-engineStopped
	}()
	handler, err := httpserver.New(a, httpserver.Config{
		Listen:         configuration.ListenAddress,
		Origin:         configuration.PublicURL,
		TrustedProxies: configuration.TrustedProxies,
		Assets:         assets,
		Logger:         logger,
		Credentials:    credentialService,
		Hosts:          hostService,
		Jobs:           jobService,
		Notifications:  notificationService,
		Executions:     engine,
		Development:    options.development,
	})
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              configuration.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	engine.StartScheduler()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		httpserver.Cleanup(ctx, db, logger)
	}()
	defer func() {
		stop()
		<-cleanupDone
	}()
	fmt.Fprintln(output, "Piped listening on", configuration.ListenAddress, "- public URL:", configuration.PublicURL)
	return errors.Join(serveHTTP(ctx, stop, server, listener, logger), engine.Err())
}

// Serve failures cancel the same lifecycle as signals. Join HTTP shutdown before
// the caller closes workers and the database; in-flight requests retain their
// graceful drain window and long-lived terminals follow the engine context.
func serveHTTP(ctx context.Context, cancel context.CancelFunc, server *http.Server, listener net.Listener, logger *slog.Logger) error {
	shutdownDone := make(chan struct{})
	context.AfterFunc(ctx, func() {
		defer close(shutdownDone)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			_ = server.Close()
		}
	})
	err := server.Serve(listener)
	cancel()
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
