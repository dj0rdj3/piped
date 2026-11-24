package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"piped/internal/store"
	"piped/internal/testdatabase"
)

func TestLoginKeepsUnknownAndMalformedIdentitiesOnThePasswordWorkPath(t *testing.T) {
	ctx := context.Background()
	driver, dsn := testdatabase.Settings(t)
	db, err := store.OpenDatabase(ctx, store.Config{Driver: driver, DSN: dsn, Path: filepath.Join(t.TempDir(), "data", "piped.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.InitAdmin(ctx, "admin", compatiblePasswordHash); err != nil {
		t.Fatal(err)
	}
	service, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		service.Hasher.slots <- struct{}{}
	}
	for _, username := range []string{"admin", "missing", "invalid username"} {
		if _, _, err := service.Login(ctx, username, "compatibility-password"); !errors.Is(err, ErrBusy) {
			t.Fatalf("%q skipped bounded password work: %v", username, err)
		}
	}
	for range 2 {
		<-service.Hasher.slots
	}
	for _, username := range []string{"missing", "invalid username"} {
		if _, token, err := service.Login(ctx, username, "compatibility-password"); !errors.Is(err, ErrCredentials) || token != "" {
			t.Fatalf("failed identity received a session or distinguishable error: %v", err)
		}
	}
	_, token, err := service.Login(ctx, " ADMIN ", "compatibility-password")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := service.Resolve(ctx, token)
	if err != nil || actor.Username != "admin" || actor.PasswordHash != compatiblePasswordHash {
		t.Fatalf("existing hashes or normalized sign-in stopped working: %v", err)
	}
}
