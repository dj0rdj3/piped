// Package hosts validates SSH host settings and encrypts per-host secrets.
package hosts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"piped/internal/secrets"
	"piped/internal/store"
)

var ErrInput = errors.New("invalid host")

type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }
func (e *InputError) Unwrap() error { return ErrInput }
func invalid(message string) error  { return &InputError{Message: message} }

type Service struct {
	store *store.Store
	vault *secrets.Vault
}

func New(ctx context.Context, db *store.Store, vault *secrets.Vault) (*Service, error) {
	if vault == nil {
		return nil, errors.New("host encryption is required")
	}
	if err := db.VerifyVault(ctx, vault); err != nil {
		return nil, err
	}
	return &Service{store: db, vault: vault}, nil
}

type Input struct {
	Name         string   `json:"name"`
	Address      string   `json:"address"`
	Port         int      `json:"port"`
	Username     string   `json:"username"`
	CredentialID string   `json:"credential_id"`
	Password     *string  `json:"password,omitempty"`
	RunAsSudo    bool     `json:"run_as_sudo"`
	SudoPassword *string  `json:"sudo_password,omitempty"`
	Tags         []string `json:"tags"`
	Version      int64    `json:"version"`
}

func boundedText(value string, limit int) bool {
	length := utf8.RuneCountInString(value)
	return utf8.ValidString(value) && length >= 1 && length <= limit && !strings.ContainsFunc(value, unicode.IsControl)
}

func validAddress(address string) bool {
	if ip, err := netip.ParseAddr(address); err == nil {
		return ip.Zone() == ""
	}
	// DNS names use ASCII labels. International names can be supplied in punycode.
	name := strings.TrimSuffix(address, ".")
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	// Reject malformed numeric IP literals rather than treating them as DNS.
	if strings.Trim(name, "0123456789.") == "" {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func validID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 16
}

func validate(input Input) (store.Host, error) {
	host := store.Host{Name: strings.TrimSpace(input.Name), Address: strings.TrimSpace(input.Address),
		Port: input.Port, Username: strings.TrimSpace(input.Username), CredentialID: input.CredentialID,
		RunAsSudo: input.RunAsSudo, Version: input.Version, Tags: []string{}}
	if !boundedText(host.Name, 100) {
		return host, invalid("name must contain 1-100 characters without control characters")
	}
	if !validAddress(host.Address) {
		return host, invalid("provide an IP address or DNS name without a scheme, path, or port")
	}
	if host.Port < 1 || host.Port > 65535 {
		return host, invalid("SSH port must be between 1 and 65535")
	}
	if !boundedText(host.Username, 128) || strings.ContainsFunc(host.Username, unicode.IsSpace) {
		return host, invalid("SSH username must contain 1-128 characters without spaces or control characters")
	}
	if host.CredentialID != "" {
		decoded, err := hex.DecodeString(host.CredentialID)
		if err != nil || len(decoded) != 16 {
			return host, invalid("invalid SSH key credential ID")
		}
		if input.Password != nil && *input.Password != "" {
			return host, invalid("choose an SSH key or a host password")
		}
	}
	for _, secret := range []*string{input.Password, input.SudoPassword} {
		if secret != nil && (len(*secret) > 1024 || !utf8.ValidString(*secret) || strings.ContainsRune(*secret, 0)) {
			return host, invalid("passwords must be valid text of at most 1024 bytes without null characters")
		}
	}
	if input.Password != nil && *input.Password == "" && host.CredentialID == "" {
		return host, invalid("SSH password must not be empty")
	}
	if !host.RunAsSudo && input.SudoPassword != nil && *input.SudoPassword != "" {
		return host, invalid("enable sudo for jobs before setting a sudo password")
	}
	if len(input.Tags) > 20 {
		return host, invalid("use at most 20 tags")
	}
	for _, tag := range input.Tags {
		tag = strings.TrimSpace(tag)
		if !boundedText(tag, 50) || strings.Contains(tag, ",") {
			return host, invalid("tags must contain 1-50 characters without commas or control characters")
		}
		host.Tags = append(host.Tags, tag)
	}
	slices.Sort(host.Tags)
	host.Tags = slices.Compact(host.Tags)
	return host, nil
}

func (s *Service) Save(ctx context.Context, actor store.Actor, id string, input Input) (store.Host, error) {
	host, err := validate(input)
	if err != nil {
		return store.Host{}, err
	}
	create := id == ""
	if create {
		if input.Version != 0 {
			return store.Host{}, invalid("new hosts must not specify a version")
		}
		bytes := make([]byte, 16)
		_, _ = rand.Read(bytes)
		id = hex.EncodeToString(bytes)
	} else if !validID(id) || input.Version < 1 {
		return store.Host{}, invalid("provide a valid host ID and current version")
	}
	host.ID = id
	var password, sudoPassword []byte
	if input.Password != nil && host.CredentialID == "" {
		plain := []byte(*input.Password)
		defer clear(plain)
		password = s.vault.Seal("host:"+id+":password", plain)
	}
	if input.SudoPassword != nil {
		sudoPassword = []byte{}
		if *input.SudoPassword != "" {
			plain := []byte(*input.SudoPassword)
			defer clear(plain)
			sudoPassword = s.vault.Seal("host:"+id+":sudo-password", plain)
		}
	}
	return s.store.SaveHost(ctx, actor, host, password, sudoPassword, create)
}

func (s *Service) List(ctx context.Context, actor store.Actor) ([]store.Host, error) {
	return s.store.Hosts(ctx, actor)
}
func (s *Service) Delete(ctx context.Context, actor store.Actor, id string, version int64) error {
	if !validID(id) || version < 1 {
		return invalid("provide a valid host ID and current version")
	}
	return s.store.DeleteHost(ctx, actor, id, version)
}
