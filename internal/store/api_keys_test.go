package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPasswordChangeInvalidatesRacingSessionAndKeyIssuance(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	admin, err := db.InitAdmin(ctx, "admin", "admin-hash")
	if err != nil {
		t.Fatal(err)
	}
	actor := actorFor(t, db, admin, "admin-session")
	owner, err := db.CreateUser(ctx, actor, "owner", "old-hash")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	start := make(chan struct{})
	errorsReceived := make(chan error, 12)
	var workers sync.WaitGroup
	for index := range 12 {
		workers.Go(func() {
			<-start
			var err error
			if index%2 == 0 {
				err = db.CreateSession(ctx, owner, fmt.Sprintf("session-%d", index), now, now+3600)
			} else {
				key := APIKey{ID: fmt.Sprintf("key-%d", index), Name: "Fixture", UserID: owner.ID, Scopes: []string{ScopeUsersRead}, CreatedAt: now, ExpiresAt: now + 3600}
				err = db.CreateLocalAPIKey(ctx, owner, key, fmt.Sprintf("key-hash-%d", index))
			}
			errorsReceived <- err
		})
	}
	close(start)
	if err := db.ChangePassword(ctx, actor, owner.ID, "new-hash"); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	close(errorsReceived)
	for err := range errorsReceived {
		if err != nil && !errors.Is(err, ErrUnauthorized) {
			t.Fatal(err)
		}
	}
	for index := range 12 {
		if index%2 == 0 {
			if _, err := db.Session(ctx, fmt.Sprintf("session-%d", index), now); !errors.Is(err, ErrNotFound) {
				t.Fatal("stale session survived the password change")
			}
		} else if _, err := db.ResolveAPIKey(ctx, fmt.Sprintf("key-hash-%d", index), now); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("stale key survived the password change")
		}
	}
	key := APIKey{ID: "fresh", Name: "Fresh", UserID: owner.ID, Scopes: []string{ScopeUsersRead}, CreatedAt: now, ExpiresAt: now + 3600}
	if err := db.CreateLocalAPIKey(ctx, owner, key, "stale-owner"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("stale owner created a key after the password change")
	}
	owner, err = db.UserByName(ctx, owner.Username)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateLocalAPIKey(ctx, owner, key, "fresh-owner"); err != nil {
		t.Fatalf("fresh owner could not create a key: %v", err)
	}
	if err := db.CreateSession(ctx, owner, "fresh-session", now, now+3600); err != nil {
		t.Fatalf("fresh owner could not create a session: %v", err)
	}
}

func TestAPIKeyPermissionsAndRevocation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	user, err := s.InitAdmin(ctx, "admin", "password-hash")
	if err != nil {
		t.Fatal(err)
	}
	key := APIKey{
		ID: "key", Name: "Read only", UserID: user.ID,
		Scopes:    []string{ScopeUsersRead, ScopeCredentialsRead},
		CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	if err := s.CreateLocalAPIKey(ctx, user, key, "token-digest"); err != nil {
		t.Fatal(err)
	}
	actor, err := s.ResolveAPIKey(ctx, "token-digest", time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Authorize(ctx, actor, ScopeUsersRead); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Credentials(ctx, actor); err != nil {
		t.Fatal(err)
	}
	actor.Scopes = append(actor.Scopes, ScopeUsersWrite, ScopeCredentialsWrite)
	if _, err := s.CreateUser(ctx, actor, "forbidden", "hash"); !errors.Is(err, ErrForbidden) {
		t.Fatal("cached actor granted user write access")
	}
	if _, err := s.CreateCredential(ctx, actor, Credential{ID: "forbidden", Name: "Forbidden"}, []byte("encrypted")); !errors.Is(err, ErrForbidden) {
		t.Fatal("cached actor granted credential write access")
	}
	encoded, _ := json.Marshal(struct {
		Keys  []APIKey
		Actor Actor
	}{[]APIKey{key}, actor})
	for _, secret := range []string{"token-digest", "session-hash", "password-hash"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("metadata serializes authentication material")
		}
	}
	if err := s.RevokeLocalAPIKey(ctx, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAPIKey(ctx, "token-digest", time.Now().Unix()); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked token resolved")
	}
	if _, err := s.Credentials(ctx, actor); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked actor read credentials")
	}
	if _, err := s.CreateUser(ctx, actor, "revoked", "hash"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked actor wrote users")
	}
}

func TestAPIKeyWritesRecheckStoredScopesExpiryAndOwner(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	user, _ := s.InitAdmin(ctx, "admin", "hash")
	admin := actorFor(t, s, user, "session")
	key := APIKey{
		ID: "key", Name: "Write", UserID: user.ID,
		Scopes:    []string{ScopeUsersWrite, ScopeCredentialsWrite},
		CreatedAt: time.Now().Add(-time.Hour).Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	if err := s.CreateLocalAPIKey(ctx, user, key, "digest"); err != nil {
		t.Fatal(err)
	}
	actor, err := s.ResolveAPIKey(ctx, "digest", time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateUser(ctx, actor, "other", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCredential(ctx, actor, Credential{ID: "record", Name: "Key"}, []byte("encrypted")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE api_keys SET scopes = ? WHERE id = ?", ScopeUsersRead, key.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, actor, other.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("scope downgrade ignored")
	}
	if err := s.RenameCredential(ctx, actor, "record", "Forbidden"); !errors.Is(err, ErrForbidden) {
		t.Fatal("credential scope downgrade ignored")
	}
	if _, err := s.db.Exec("UPDATE api_keys SET expires_at = ? WHERE id = ?", time.Now().Unix()-1, key.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCredential(ctx, actor, "record"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expiry ignored by write transaction")
	}
	if _, err := s.ResolveAPIKey(ctx, "digest", key.ExpiresAt); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("expiry boundary accepted")
	}
	if err := s.PurgeAPIKeys(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&count); err != nil || count != 0 {
		t.Fatal("expired key was not purged")
	}
	key.ID = "new"
	if err := s.CreateLocalAPIKey(ctx, user, key, "new-digest"); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, admin, user.ID, "new-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAPIKey(ctx, "new-digest", time.Now().Unix()); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("password change kept API key")
	}
	if err := s.CreateLocalAPIKey(ctx, user, key, "stale"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("CLI used stale owner password")
	}
}

func TestDeletingAPIKeyOwnerRevokesCachedAccess(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	admin, err := s.InitAdmin(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	session := actorFor(t, s, admin, "session")
	owner, err := s.CreateUser(ctx, session, "developer", "hash")
	if err != nil {
		t.Fatal(err)
	}
	key := APIKey{
		ID: "owned-key", Name: "Owned key", UserID: owner.ID, Scopes: []string{ScopeUsersRead},
		CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}
	if err := s.CreateLocalAPIKey(ctx, admin, key, "digest"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("local issuer accepted a mismatched owner")
	}
	if err := s.CreateLocalAPIKey(ctx, owner, key, "digest"); err != nil {
		t.Fatal(err)
	}
	actor, err := s.ResolveAPIKey(ctx, "digest", time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, session, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateLocalAPIKey(ctx, owner, key, "deleted-owner"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("deleted owner received a new key")
	}
	if err := s.Authorize(ctx, actor, ScopeUsersRead); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("deleted owner's cached actor still authorized")
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&count); err != nil || count != 0 {
		t.Fatal("owner deletion left orphaned keys")
	}
}

func TestPersonalKeyManagementRejectsBearerStaleAndForeignAuthority(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	owner, err := s.InitAdmin(ctx, "owner", "owner-hash")
	if err != nil {
		t.Fatal(err)
	}
	session := actorFor(t, s, owner, "owner-session")
	now := time.Now().Unix()
	key := APIKey{ID: "personal", UserID: owner.ID, Purpose: "production", Name: "Personal", Scopes: []string{ScopeUsersRead}, CreatedAt: now, ExpiresAt: now + 3600}
	if err := s.CreateUserAPIKey(ctx, session, key, "personal-digest"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeLocalAPIKey(ctx, key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("internal CLI revoked a personal key")
	}
	bearer, err := s.ResolveAPIKey(ctx, "personal-digest", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserAPIKeys(ctx, bearer); !errors.Is(err, ErrForbidden) {
		t.Fatal("bearer listed keys")
	}
	if err := s.RevokeUserAPIKey(ctx, bearer, key.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("bearer revoked a key")
	}
	other, err := s.CreateUser(ctx, session, "other", "other-hash")
	if err != nil {
		t.Fatal(err)
	}
	otherSession := actorFor(t, s, other, "other-session")
	if err := s.CreateUserAPIKey(ctx, otherSession, key, "wrong-owner"); !errors.Is(err, ErrForbidden) {
		t.Fatal("key created for another owner")
	}
	if err := s.RevokeUserAPIKey(ctx, otherSession, key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-owner revocation")
	}
	if err := s.DeleteSession(ctx, session.SessionHash); err != nil {
		t.Fatal(err)
	}
	key.ID = "stale-key"
	if err := s.CreateUserAPIKey(ctx, session, key, "stale-digest"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("stale session created key")
	}
	if err := s.RevokeUserAPIKey(ctx, session, "personal"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("stale session revoked key")
	}
}

func TestNoExpirationKeysSurviveCleanupAndHonorRevocation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	owner, err := s.InitAdmin(ctx, "owner", "owner-hash")
	if err != nil {
		t.Fatal(err)
	}
	session := actorFor(t, s, owner, "owner-session")
	key := APIKey{ID: "no-expiration", Name: "No expiration", Purpose: "production", UserID: owner.ID, Scopes: []string{ScopeUsersRead, ScopeUsersWrite}, CreatedAt: time.Now().Unix()}
	if err := s.CreateUserAPIKey(ctx, session, key, "no-expiration-digest"); err != nil {
		t.Fatal(err)
	}
	var expiry sql.NullInt64
	if err := s.db.QueryRow("SELECT expires_at FROM api_keys WHERE id = ?", key.ID).Scan(&expiry); err != nil || expiry.Valid {
		t.Fatalf("no-expiration key must use NULL in storage: %v", err)
	}
	future := time.Now().AddDate(100, 0, 0)
	if err := s.PurgeAPIKeys(ctx, future); err != nil {
		t.Fatal(err)
	}
	actor, err := s.ResolveAPIKey(ctx, "no-expiration-digest", future.Unix())
	if err != nil {
		t.Fatal("no-expiration key expired or was purged", err)
	}
	keys, err := s.UserAPIKeys(ctx, session)
	if err != nil || len(keys) != 1 || keys[0].ExpiresAt != 0 {
		t.Fatalf("wrong no-expiration metadata: %v", err)
	}
	if _, err := s.CreateUser(ctx, actor, "created-with-key", "hash"); err != nil {
		t.Fatal("no-expiration key failed transactional authorization", err)
	}
	if err := s.RevokeUserAPIKey(ctx, session, key.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Authorize(ctx, actor, ScopeUsersRead); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked no-expiration key remained authorized")
	}
	key.ID = "password-revoked"
	if err := s.CreateUserAPIKey(ctx, session, key, "password-digest"); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, session, owner.ID, "new-owner-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAPIKey(ctx, "password-digest", time.Now().Unix()); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("password change retained a no-expiration key")
	}
}
