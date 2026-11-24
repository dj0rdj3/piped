package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

var (
	ErrPassword     = errors.New("password must contain 8-128 characters")
	ErrUsername     = errors.New("username must be 3-64 lowercase letters, digits, dots, underscores, or hyphens")
	ErrBusy         = errors.New("authentication is busy; try again shortly")
	usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)
)

const passwordHashPrefix = "$argon2id$v=19$m=65536,t=3,p=1$"

func NormalizeUsername(username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) {
		return "", ErrUsername
	}
	return username, nil
}

func ValidatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 8 || utf8.RuneCountInString(password) > 128 {
		return ErrPassword
	}
	return nil
}

// Hasher limits expensive operations to bound memory use under concurrent requests.
type Hasher struct{ slots chan struct{} }

func NewHasher() *Hasher { return &Hasher{slots: make(chan struct{}, 2)} }

func (h *Hasher) deriveKey(password string, salt []byte) ([]byte, error) {
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		return nil, ErrBusy
	}
	return argon2.IDKey([]byte(password), salt, 3, 64*1024, 1, 32), nil
}

func (h *Hasher) Hash(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := h.deriveKey(password, salt)
	if err != nil {
		return "", err
	}
	return passwordHashPrefix + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func (h *Hasher) Verify(encoded, password string) (bool, error) {
	// Accept only this implementation's bounded parameters, never attacker-controlled costs.
	payload, supported := strings.CutPrefix(encoded, passwordHashPrefix)
	parts := strings.Split(payload, "$")
	if !supported || len(parts) != 2 {
		return false, errors.New("unsupported password hash")
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[0])
	if err != nil || len(salt) != 16 {
		return false, errors.New("invalid password salt")
	}
	expected, err := base64.RawStdEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(expected) != 32 {
		return false, errors.New("invalid password hash")
	}
	if len(password) > 512 {
		return false, nil
	}
	actual, err := h.deriveKey(password, salt)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}
