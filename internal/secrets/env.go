package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

const envKey = "PIPED_ENCRYPTION_KEY"

// LoadEnv reads a deliberately small .env format, without shell execution,
// interpolation, or process-environment overrides.
func LoadEnv(path string) (*Vault, error) {
	configuration, err := LoadConfiguration(path)
	if err != nil {
		return nil, err
	}
	return configuration.Vault, nil
}

type Configuration struct {
	Vault            *Vault
	DatabaseDriver   string
	DatabaseDSN      string
	DatabasePath     string
	ListenAddress    string
	PublicURL        string
	LogRetentionDays int
	TrustedProxies   []netip.Prefix
}

// LoadConfiguration requires a deployment key before the server can start.
func LoadConfiguration(path string) (*Configuration, error) {
	configuration, err := LoadSettings(path)
	if err != nil {
		return nil, err
	}
	if configuration.Vault == nil {
		return nil, errors.New(".env must contain PIPED_ENCRYPTION_KEY (create it with piped keygen)")
	}
	return configuration, nil
}

// LoadSettings lets setup commands select the configured database before a key exists.
// An existing file must still satisfy all format and permission checks.
func LoadSettings(path string) (*Configuration, error) {
	configuration := &Configuration{
		DatabaseDriver:   "sqlite",
		DatabasePath:     "data/piped.db",
		ListenAddress:    "127.0.0.1:8080",
		PublicURL:        "http://127.0.0.1:8080",
		LogRetentionDays: 7,
		TrustedProxies:   []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return configuration, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read .env file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New(".env must be a regular, private file (chmod 600); symlinks are not allowed")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) || opened.Mode().Perm()&0077 != 0 {
		return nil, errors.New(".env changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return nil, err
	}
	defer clear(data)
	if len(data) > 4096 {
		return nil, errors.New(".env file is too large")
	}
	seen := make(map[string]bool)
	for line := range bytes.Lines(data) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		name, value, ok := bytes.Cut(line, []byte("="))
		if !ok {
			return nil, errors.New("invalid .env assignment")
		}
		setting := string(bytes.TrimSpace(name))
		if seen[setting] {
			return nil, errors.New("duplicate .env assignment")
		}
		seen[setting] = true
		value = bytes.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if bytes.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New(".env values must not contain control characters")
		}
		switch setting {
		case envKey:
			key := make([]byte, base64.StdEncoding.DecodedLen(len(value)))
			defer clear(key)
			n, err := base64.StdEncoding.Strict().Decode(key, value)
			if err != nil {
				return nil, errors.New("PIPED_ENCRYPTION_KEY must be a base64-encoded 32-byte random key")
			}
			vault, err := New(key[:n])
			if err != nil {
				return nil, err
			}
			configuration.Vault = vault
		case "PIPED_DATABASE_DRIVER":
			if len(value) > 0 {
				configuration.DatabaseDriver = string(value)
			}
		case "PIPED_DATABASE_DSN":
			configuration.DatabaseDSN = string(value)
		case "PIPED_DATABASE_PATH":
			if len(value) > 0 {
				configuration.DatabasePath = string(value)
			}
		case "PIPED_LISTEN":
			if len(value) > 0 {
				configuration.ListenAddress = string(value)
			}
		case "PIPED_PUBLIC_URL":
			if len(value) > 0 {
				configuration.PublicURL = string(value)
			}
		case "PIPED_TRUSTED_PROXIES":
			configuration.TrustedProxies, err = parseTrustedProxies(string(value))
			if err != nil {
				return nil, err
			}
		case "PIPED_LOG_RETENTION_DAYS":
			if len(value) > 0 {
				parsed, err := strconv.Atoi(string(value))
				if err != nil || parsed < 1 || parsed > 36500 {
					return nil, errors.New("PIPED_LOG_RETENTION_DAYS must be between 1 and 36500")
				}
				configuration.LogRetentionDays = parsed
			}
		default:
			return nil, errors.New("unsupported .env setting")
		}
	}
	switch configuration.DatabaseDriver {
	case "sqlite":
		if configuration.DatabaseDSN != "" {
			return nil, errors.New("PIPED_DATABASE_DSN must be empty for sqlite; use PIPED_DATABASE_PATH")
		}
	case "postgres", "mariadb", "mysql":
		if configuration.DatabaseDSN == "" {
			return nil, errors.New("PIPED_DATABASE_DSN is required for server databases")
		}
	default:
		return nil, errors.New("PIPED_DATABASE_DRIVER must be sqlite, postgres, mariadb, or mysql")
	}
	return configuration, nil
}

func parseTrustedProxies(value string) ([]netip.Prefix, error) {
	if value == "" {
		return nil, nil
	}
	var prefixes []netip.Prefix
	for entry := range strings.SplitSeq(value, ",") {
		entry = strings.TrimSpace(entry)
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			address, err := netip.ParseAddr(entry)
			if err != nil || address.Zone() != "" {
				return nil, errors.New("PIPED_TRUSTED_PROXIES must contain comma-separated IP addresses or CIDR ranges")
			}
			address = address.Unmap()
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, errors.New("PIPED_TRUSTED_PROXIES has an invalid IPv4-mapped CIDR range")
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

// GenerateEnv creates a new key file exclusively; existing files are never replaced.
func GenerateEnv(path string) error {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	defer clear(key)
	data := []byte(envKey + "=" + base64.StdEncoding.EncodeToString(key) + "\n" +
		"PIPED_DATABASE_DRIVER=sqlite\n" +
		"PIPED_DATABASE_DSN=\n" +
		"PIPED_DATABASE_PATH=data/piped.db\n" +
		"PIPED_LISTEN=127.0.0.1:8080\n" +
		"PIPED_PUBLIC_URL=http://127.0.0.1:8080\n" +
		"PIPED_TRUSTED_PROXIES=127.0.0.0/8\n" +
		"PIPED_LOG_RETENTION_DAYS=7\n")
	defer clear(data)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create encryption .env: %w", err)
	}
	// Remove an incomplete file so it cannot be mistaken for a usable key file.
	ok := false
	defer func() {
		f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
