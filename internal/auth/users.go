package auth

import (
	"context"

	"piped/internal/store"
)

// User mutations share validation and hashing across browser and MCP handlers.
// The store rechecks the actor inside the transaction that changes the account.
func (s *Service) CreateUser(ctx context.Context, actor store.Actor, username, password string) (store.User, error) {
	name, err := NormalizeUsername(username)
	if err != nil {
		return store.User{}, err
	}
	hash, err := s.Hasher.Hash(password)
	if err != nil {
		return store.User{}, err
	}
	return s.Store.CreateUser(ctx, actor, name, hash)
}

func (s *Service) ChangePassword(ctx context.Context, actor store.Actor, id int64, password string) error {
	hash, err := s.Hasher.Hash(password)
	if err != nil {
		return err
	}
	return s.Store.ChangePassword(ctx, actor, id, hash)
}
