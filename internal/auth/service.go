package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"piped/internal/store"
)

var ErrCredentials = errors.New("invalid username or password")

const SessionLifetime = 8 * time.Hour

type Service struct {
	Store     *store.Store
	Hasher    *Hasher
	dummyHash string
}

func New(s *store.Store) (*Service, error) {
	h := NewHasher()
	dummy, err := h.Hash(base64.RawURLEncoding.EncodeToString(randomBytes()))
	if err != nil {
		return nil, err
	}
	return &Service{Store: s, Hasher: h, dummyHash: dummy}, nil
}

func randomBytes() []byte {
	b := make([]byte, 32)
	// crypto/rand.Read terminates on an operating-system entropy failure in Go 1.24+.
	_, _ = rand.Read(b)
	return b
}

func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Service) Login(ctx context.Context, username, password string) (store.User, string, error) {
	name, nameErr := NormalizeUsername(username)
	u, err := s.Store.UserByName(ctx, name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.User{}, "", err
	}
	hash := u.PasswordHash
	if errors.Is(err, store.ErrNotFound) || nameErr != nil {
		hash = s.dummyHash
	}
	ok, err := s.Hasher.Verify(hash, password)
	if err != nil {
		return store.User{}, "", err
	}
	if !ok || u.ID == 0 || nameErr != nil {
		return store.User{}, "", ErrCredentials
	}
	token := base64.RawURLEncoding.EncodeToString(randomBytes())
	now := time.Now()
	if err := s.Store.CreateSession(ctx, u, TokenHash(token), now.Unix(), now.Add(SessionLifetime).Unix()); err != nil {
		if errors.Is(err, store.ErrUnauthorized) {
			return store.User{}, "", ErrCredentials
		}
		return store.User{}, "", err
	}
	return u, token, nil
}

func (s *Service) Resolve(ctx context.Context, token string) (store.Actor, error) {
	if len(token) != 43 {
		return store.Actor{}, store.ErrUnauthorized
	}
	return s.Store.Session(ctx, TokenHash(token), time.Now().Unix())
}

func (s *Service) Reauthenticate(actor store.Actor, password string) error {
	ok, err := s.Hasher.Verify(actor.PasswordHash, password)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCredentials
	}
	return nil
}

func (s *Service) InitAdmin(ctx context.Context, username, password string) (store.User, error) {
	name, err := NormalizeUsername(username)
	if err != nil {
		return store.User{}, err
	}
	hash, err := s.Hasher.Hash(password)
	if err != nil {
		return store.User{}, err
	}
	return s.Store.InitAdmin(ctx, name, hash)
}
