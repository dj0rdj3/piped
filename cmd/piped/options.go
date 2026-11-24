package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"piped/internal/httpserver"
	"piped/internal/secrets"
)

type cliOptions struct {
	command       string
	envFile       string
	database      string
	listen        string
	publicURL     string
	development   bool
	username      string
	passwordStdin bool
	keyName       string
	keyScopes     string
	keyLifetime   time.Duration
	keyJSON       bool
	keyRevoke     string
}

func parseOptions(args []string, output io.Writer) (cliOptions, error) {
	options := cliOptions{command: "serve", envFile: ".env"}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		options.command, args = args[0], args[1:]
	}
	switch options.command {
	case "serve", "register", "keygen":
	case "init-admin", "api-key":
		if !httpserver.DevelopmentBuild {
			return options, errors.New("this setup command is available only in piped-dev")
		}
	default:
		return options, errors.New("unknown command; run piped, piped register, or piped keygen")
	}

	flags := flag.NewFlagSet(options.command, flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		binary := "piped"
		if httpserver.DevelopmentBuild {
			binary = "piped-dev"
		}
		if options.command == "serve" {
			fmt.Fprintln(output, "Usage:", binary)
			fmt.Fprintln(output, "      ", binary, "register")
			fmt.Fprintln(output, "      ", binary, "keygen")
			fmt.Fprintln(output, "\nRun without a command to start the server. Runtime settings are read from .env.")
			if httpserver.DevelopmentBuild {
				fmt.Fprintln(output, "Development setup: init-admin, api-key. Flags override .env for this invocation.")
			}
		} else {
			fmt.Fprintln(output, "Usage:", binary, options.command)
		}
		flags.PrintDefaults()
	}
	if httpserver.DevelopmentBuild {
		flags.StringVar(&options.envFile, "env-file", ".env", "temporary .env file override")
		if options.command != "keygen" {
			flags.StringVar(&options.database, "db", "", "temporary SQLite database path override")
		}
		if options.command == "serve" {
			flags.BoolVar(&options.development, "development", false, "enable loopback-only development integrations")
			flags.StringVar(&options.listen, "listen", "", "temporary listen address override")
			flags.StringVar(&options.publicURL, "public-url", "", "temporary public origin override")
		}
	}
	if options.command == "init-admin" {
		flags.StringVar(&options.username, "username", "admin", "initial administrator username")
		flags.BoolVar(&options.passwordStdin, "password-stdin", false, "read the password from standard input (required)")
	}
	if options.command == "api-key" {
		flags.StringVar(&options.username, "username", "", "existing app user who owns the key (required)")
		flags.StringVar(&options.keyName, "name", "Development", "API key label")
		flags.StringVar(&options.keyScopes, "scopes", "users:read,credentials:read,hosts:read", "comma-separated development API scopes")
		flags.DurationVar(&options.keyLifetime, "ttl", 7*24*time.Hour, "key lifetime (1h to 2160h)")
		flags.BoolVar(&options.keyJSON, "json", false, "return key metadata and token as JSON for internal automation")
		flags.StringVar(&options.keyRevoke, "revoke", "", "revoke a development key by ID")
	}
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() > 0 {
		return options, errors.New("unexpected positional arguments")
	}
	emptyOverride := false
	flags.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "db", "listen", "public-url", "env-file":
			emptyOverride = emptyOverride || option.Value.String() == ""
		}
	})
	if emptyOverride {
		return options, errors.New("development configuration overrides must not be empty")
	}
	return options, nil
}

func loadConfiguration(options cliOptions) (*secrets.Configuration, error) {
	var configuration *secrets.Configuration
	var err error
	if options.command == "serve" {
		configuration, err = secrets.LoadConfiguration(options.envFile)
	} else {
		configuration, err = secrets.LoadSettings(options.envFile)
	}
	if err != nil {
		return nil, err
	}
	if options.database != "" {
		configuration.DatabaseDriver = "sqlite"
		configuration.DatabaseDSN = ""
		configuration.DatabasePath = options.database
	}
	if options.listen != "" {
		configuration.ListenAddress = options.listen
	}
	if options.publicURL != "" {
		configuration.PublicURL = options.publicURL
	}
	if options.command == "serve" {
		if err := httpserver.ValidateConfig(httpserver.Config{
			Listen: configuration.ListenAddress, Origin: configuration.PublicURL,
			Development:    options.development,
			TrustedProxies: configuration.TrustedProxies,
		}); err != nil {
			return nil, err
		}
	}
	return configuration, nil
}
