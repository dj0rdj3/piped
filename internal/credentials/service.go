// Package credentials validates SSH keys and stores only encrypted secret material.
package credentials

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
	"piped/internal/secrets"
	"piped/internal/store"
)

var (
	ErrInput = errors.New("invalid credential")
	ErrBusy  = errors.New("key processing is busy; try again")
)

type InputError struct{ Message string }

func (e *InputError) Error() string { return e.Message }
func (e *InputError) Unwrap() error { return ErrInput }
func invalid(message string) error  { return &InputError{Message: message} }

type Service struct {
	store    *store.Store
	vault    *secrets.Vault
	workers  chan struct{}
	hostname string
}

func New(ctx context.Context, db *store.Store, vault *secrets.Vault) (*Service, error) {
	if vault == nil {
		return nil, errors.New("credential encryption is required")
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("read system hostname: %w", err)
	}
	if err := db.VerifyVault(ctx, vault); err != nil {
		return nil, err
	}
	return &Service{store: db, vault: vault, workers: make(chan struct{}, 2), hostname: hostname}, nil
}

func Name(name string) (string, error) {
	name = strings.TrimSpace(name)
	length := utf8.RuneCountInString(name)
	if !utf8.ValidString(name) || length < 1 || length > 100 {
		return "", invalid("name must contain 1-100 characters")
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return "", invalid("name must not contain control characters")
	}
	return name, nil
}

type CreateInput struct {
	Name       string `json:"name"`
	Mode       string `json:"mode"`
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	Passphrase string `json:"passphrase"`
}

func (s *Service) Create(ctx context.Context, actor store.Actor, input CreateInput) (store.Credential, error) {
	name, err := validateCreateInput(input)
	if err != nil {
		return store.Credential{}, err
	}
	select {
	case s.workers <- struct{}{}:
		defer func() { <-s.workers }()
	default:
		return store.Credential{}, ErrBusy
	}
	if err := ctx.Err(); err != nil {
		return store.Credential{}, err
	}
	var raw any
	var private []byte
	defer func() { clear(private) }()
	if input.Mode == "generate" {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return store.Credential{}, err
		}
		defer clear(key)
		raw = key
		block, err := ssh.MarshalPrivateKey(key, "")
		if err != nil {
			return store.Credential{}, err
		}
		defer clear(block.Bytes)
		private = pem.EncodeToMemory(block)
	} else {
		private = []byte(input.PrivateKey)
		if err := checkPEM(private); err != nil {
			return store.Credential{}, err
		}
		raw, err = ssh.ParseRawPrivateKey(private)
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			if input.Passphrase == "" {
				return store.Credential{}, invalid("this private key requires a passphrase")
			}
			passphrase := []byte(input.Passphrase)
			defer clear(passphrase)
			raw, err = ssh.ParseRawPrivateKeyWithPassphrase(private, passphrase)
		} else if err == nil && input.Passphrase != "" {
			return store.Credential{}, invalid("this private key is not passphrase-protected; leave the passphrase empty")
		}
		if err != nil {
			return store.Credential{}, invalid("invalid or unsupported private key, or incorrect passphrase")
		}
	}
	if err := validateKey(raw); err != nil {
		return store.Credential{}, err
	}
	signer, err := ssh.NewSignerFromKey(raw)
	if err != nil {
		return store.Credential{}, invalid("unsupported private key")
	}
	// Verify a signature to reject inconsistent private/public components in
	// otherwise parseable key formats (notably OpenSSH Ed25519 key files).
	challenge := make([]byte, 32)
	_, _ = rand.Read(challenge)
	sig, err := signer.Sign(rand.Reader, challenge)
	if err != nil || signer.PublicKey().Verify(challenge, sig) != nil {
		return store.Credential{}, invalid("private key components do not match")
	}
	publicKey, err := authorizedPublicKey(signer.PublicKey(), input.PublicKey, s.comment(actor))
	if err != nil {
		return store.Credential{}, err
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	c := store.Credential{
		ID:            hex.EncodeToString(id),
		Name:          name,
		PublicKey:     publicKey,
		Fingerprint:   ssh.FingerprintSHA256(signer.PublicKey()),
		Algorithm:     signer.PublicKey().Type(),
		HasPassphrase: input.Passphrase != "",
	}
	secret, err := json.Marshal(struct {
		PrivateKey string `json:"private_key"`
		Passphrase string `json:"passphrase"`
	}{string(private), input.Passphrase})
	if err != nil {
		return store.Credential{}, err
	}
	defer clear(secret)
	return s.store.CreateCredential(ctx, actor, c, s.vault.Seal("credential:"+c.ID+":secret", secret))
}

func validateCreateInput(input CreateInput) (string, error) {
	name, err := Name(input.Name)
	if err != nil {
		return "", err
	}
	if input.Mode != "generate" && input.Mode != "import" {
		return "", invalid("choose import or generate")
	}
	if len(input.Passphrase) > 1024 || !utf8.ValidString(input.Passphrase) {
		return "", invalid("key passphrase is too long or invalid")
	}
	if input.Mode == "generate" && (input.PrivateKey != "" || input.PublicKey != "" || input.Passphrase != "") {
		return "", invalid("generated keys do not accept key or passphrase input")
	}
	if len(input.PublicKey) > 8192 {
		return "", invalid("public key must contain at most 8 KiB")
	}
	if input.Mode == "import" && (len(input.PrivateKey) == 0 || len(input.PrivateKey) > 32*1024) {
		return "", invalid("private key must contain at most 32 KiB")
	}
	return name, nil
}

func authorizedPublicKey(derived ssh.PublicKey, suppliedKey, comment string) (string, error) {
	if supplied := strings.TrimSpace(suppliedKey); supplied != "" {
		if strings.ContainsAny(supplied, "\r\n") {
			return "", invalid("provide exactly one SSH public key")
		}
		pub, originalComment, options, rest, err := ssh.ParseAuthorizedKey([]byte(supplied))
		if err != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
			return "", invalid("provide exactly one SSH public key without authorized_keys options")
		}
		if !bytes.Equal(pub.Marshal(), derived.Marshal()) {
			return "", invalid("public key does not match the private key")
		}
		for _, r := range originalComment {
			if unicode.IsControl(r) && r != '\t' {
				return "", invalid("public key comment contains control characters")
			}
		}
		comment = originalComment
	}
	publicKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(derived)))
	if comment != "" {
		publicKey += " " + comment
	}
	return publicKey, nil
}

func validateKey(raw any) error {
	switch key := raw.(type) {
	case *rsa.PrivateKey:
		if key.N.BitLen() < 2048 || key.N.BitLen() > 8192 {
			return invalid("RSA keys must be between 2048 and 8192 bits")
		}
	case *ecdsa.PrivateKey:
		if key.Curve.Params().BitSize < 256 {
			return invalid("ECDSA keys must be at least 256 bits")
		}
	case ed25519.PrivateKey, *ed25519.PrivateKey:
	default:
		return invalid("only RSA, ECDSA, and Ed25519 keys are supported")
	}
	return nil
}

// Bound attacker-controlled bcrypt work before handing an OpenSSH import to the
// SSH parser. The library accepts arbitrary round counts; web requests cannot.
func checkPEM(private []byte) error {
	block, rest := pem.Decode(private)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 || !bytes.HasPrefix(bytes.TrimSpace(private), []byte("-----BEGIN "+block.Type+"-----")) {
		return invalid("provide exactly one PEM-encoded private key")
	}
	defer clear(block.Bytes)
	if block.Type != "OPENSSH PRIVATE KEY" {
		return nil
	}
	const magic = "openssh-key-v1\x00"
	if !bytes.HasPrefix(block.Bytes, []byte(magic)) {
		return invalid("invalid OpenSSH private key")
	}
	var envelope struct {
		Cipher  string
		KDF     string
		Options string
		Keys    uint32
		Rest    []byte `ssh:"rest"`
	}
	if ssh.Unmarshal(block.Bytes[len(magic):], &envelope) != nil || envelope.Keys != 1 {
		return invalid("invalid OpenSSH private key")
	}
	if envelope.KDF == "none" {
		return nil
	}
	var kdf struct {
		Salt   string
		Rounds uint32
	}
	if envelope.KDF != "bcrypt" || ssh.Unmarshal([]byte(envelope.Options), &kdf) != nil || kdf.Rounds < 1 || kdf.Rounds > 64 || len(kdf.Salt) > 64 {
		return invalid("unsupported key encryption settings; OpenSSH bcrypt imports support 1-64 rounds")
	}
	return nil
}

func (s *Service) List(ctx context.Context, actor store.Actor) ([]store.Credential, error) {
	return s.store.Credentials(ctx, actor)
}

func (s *Service) comment(actor store.Actor) string {
	// Comments must stay on one authorized_keys line, even with an unusual
	// operating-system hostname. Usernames normally already use this alphabet.
	safe := func(part string) string {
		return strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
				return r
			}
			return '_'
		}, part)
	}
	return safe(actor.Username) + "@" + safe(s.hostname)
}

func (s *Service) Rename(ctx context.Context, actor store.Actor, id, name string) error {
	name, err := Name(name)
	if err != nil {
		return err
	}
	return s.store.RenameCredential(ctx, actor, id, name)
}

func (s *Service) Delete(ctx context.Context, actor store.Actor, id string) error {
	return s.store.DeleteCredential(ctx, actor, id)
}
