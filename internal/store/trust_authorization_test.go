package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTrustMutationRechecksBothScopesAndRevocation(t *testing.T) {
	db, actor, _ := admissionFixture(t)
	ctx := context.Background()
	now := time.Now().Unix()
	key := APIKey{ID: "trust-key", Name: "Trust", UserID: actor.ID, CreatedAt: now, ExpiresAt: now + 3600, Scopes: []string{ScopeHostsWrite}}
	if err := db.CreateLocalAPIKey(ctx, actor.User, key, "trust-hash"); err != nil {
		t.Fatal(err)
	}
	caller, err := db.ResolveAPIKey(ctx, "trust-hash", now)
	if err != nil {
		t.Fatal(err)
	}
	// Forging a cached scope cannot upgrade the authorized transaction.
	caller.Scopes = append(caller.Scopes, ScopeRunsWrite)
	if err := db.ApproveHostKey(ctx, caller, "host", "localhost", 22, "reviewed-key"); !errors.Is(err, ErrForbidden) {
		t.Fatal("trust write skipped fresh runs authority", err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE api_keys SET scopes = ? WHERE id = ?`, ScopeHostsWrite+","+ScopeRunsWrite, key.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ApproveHostKey(ctx, caller, "host", "localhost", 22, "reviewed-key"); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeLocalAPIKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ApproveHostKey(ctx, caller, "host", "localhost", 22, "replacement-key"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked authority changed trust", err)
	}
	trusted, err := db.TrustedHostKey(ctx, "host", "localhost", 22)
	if err != nil || trusted != "reviewed-key" {
		t.Fatal("failed transaction changed trust", trusted, err)
	}
}
