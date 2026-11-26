package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func listActor(t *testing.T, db *Store) Actor {
	t.Helper()
	user, err := db.InitAdmin(context.Background(), "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	return actorFor(t, db, user, "list-session")
}

func TestStoragePagesPreserveUnicodeOrderAndDeletedCursor(t *testing.T) {
	db := openTest(t)
	actor := listActor(t, db)
	ctx := context.Background()
	names := []string{"Zulu", "\u00c9clair", "\u00e9cole", "Alpha", "alpha two", "\u03a9mega", "\u03c9 two", "z%_b"}
	expected := []string{}
	for _, name := range names {
		_, err := db.CreateCredential(ctx, actor, Credential{ID: name, Name: name, Algorithm: "ssh-ed25519", Fingerprint: "fingerprint"}, []byte("ciphertext"))
		if err != nil {
			t.Fatal(err)
		}
		expected = append(expected, name)
	}
	sort.Slice(expected, func(i, j int) bool {
		return strings.ToLower(expected[i])+"\x00"+expected[i] < strings.ToLower(expected[j])+"\x00"+expected[j]
	})
	request := ListRequest{Limit: 1}
	seen := []string{}
	for {
		page, err := db.CredentialsPage(ctx, actor, request)
		if err != nil || len(page.Items) != 1 {
			t.Fatalf("page: %+v %v", page, err)
		}
		seen = append(seen, page.Items[0].Name)
		if len(seen) == 1 {
			if err := db.DeleteCredential(ctx, actor, page.Items[0].ID); err != nil {
				t.Fatal(err)
			}
		}
		if page.Info.NextCursor == "" {
			break
		}
		request.Cursor = page.Info.NextCursor
	}
	if !slices.Equal(seen, expected) {
		t.Fatalf("order/deletion: %q != %q", seen, expected)
	}
	for _, query := range []string{"\u00c9C", "\u03a9", "%_"} {
		page, err := db.CredentialsPage(ctx, actor, ListRequest{Limit: 2, Query: query})
		if err != nil || page.Info.Matched == 0 {
			t.Fatalf("Unicode/literal search %q: %+v %v", query, page, err)
		}
	}
	for _, request := range []ListRequest{{Limit: -1}, {Limit: 201}, {Limit: 1, Cursor: "invalid"}, {Limit: 1, IDs: make([]string, 101)}, {Limit: 1, Query: "\x00"}, {Limit: 1, Tag: "\xff"}, {Limit: 1, IDs: []string{"\x00"}}} {
		if _, err := db.CredentialsPage(ctx, actor, request); !errors.Is(err, ErrListRequest) {
			t.Fatalf("invalid request: %v", err)
		}
	}
}

func TestStoragePagesLoadOnlySelectedPublicRows(t *testing.T) {
	db := openTest(t)
	actor := listActor(t, db)
	ctx := context.Background()
	for _, id := range []string{"a", "z"} {
		if _, err := db.SaveHost(ctx, actor, Host{ID: id, Name: id, Address: "localhost", Port: 22, Username: "user", Tags: []string{}}, []byte("ciphertext"), nil, true); err != nil {
			t.Fatal(err)
		}
	}
	// Assert the loader receives only selected IDs, independently of row encoding.
	selectedOnly := func(ctx context.Context, tx *authorizedTransaction, actor Actor, ids []string) ([]Host, error) {
		if !slices.Equal(ids, []string{"a"}) {
			t.Fatalf("loader received unbounded IDs: %q", ids)
		}
		return readHosts(ctx, tx, actor, ids)
	}

	page, err := readPage(ctx, db, actor, "hosts", ScopeHostsRead, ListRequest{Limit: 1}, selectedOnly, func(host Host) string { return host.ID })
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "a" || page.Info.Total != 2 {
		t.Fatalf("unbounded loader: %+v %v", page, err)
	}
	selected, err := db.HostsPage(ctx, actor, ListRequest{IDs: []string{"a"}})
	if err != nil || len(selected.Items) != 1 || selected.Items[0].ID != "a" {
		t.Fatalf("explicit IDs loaded unrelated rows: %+v %v", selected, err)
	}
	unknown, err := db.UsersPage(ctx, actor, ListRequest{IDs: []string{"not-an-integer"}})
	if err != nil || len(unknown.Items) != 0 {
		t.Fatalf("unknown ID was cast to a SQL integer: %+v %v", unknown, err)
	}
}

func TestListMetadataTracksRelationshipsAndAuthorization(t *testing.T) {
	db := openTest(t)
	actor := listActor(t, db)
	ctx := context.Background()
	credential, err := db.CreateCredential(ctx, actor, Credential{ID: "key", Name: "Original credential", PublicKey: "key", Algorithm: "ssh-ed25519", Fingerprint: "fingerprint"}, []byte("ciphertext"))
	if err != nil {
		t.Fatal(err)
	}
	host, err := db.SaveHost(ctx, actor, Host{ID: "host", Name: "Private host", Address: "localhost", Port: 22, Username: "user", CredentialID: credential.ID, Tags: []string{"old-tag"}}, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	job, err := db.SaveJob(ctx, actor, Job{ID: "job", Name: "Definition", Script: "secret script must not be indexed", HostIDs: []string{}, HostTags: []string{"new-tag"}, ExecutionMode: "sequential"}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	count := func(query string, want int) {
		t.Helper()
		page, err := db.JobsPage(ctx, actor, ListRequest{Limit: 1, Query: query})
		if err != nil || page.Info.Matched != want {
			t.Fatalf("query %q: %+v %v", query, page, err)
		}
	}
	count("Private", 0)
	host.Tags = []string{"new-tag"}
	host, err = db.SaveHost(ctx, actor, host, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	count("Private", 1)
	count("new-tag Private", 1)
	count("secret script", 0)
	host.Name = "Renamed host"
	host, err = db.SaveHost(ctx, actor, host, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	count("Private", 0)
	count("Renamed", 1)
	if err := db.RenameCredential(ctx, actor, credential.ID, "Renamed credential"); err != nil {
		t.Fatal(err)
	}
	page, err := db.HostsPage(ctx, actor, ListRequest{Limit: 1, Query: "Renamed credential", Tag: "new-tag"})
	if err != nil || page.Info.Matched != 1 || !slices.Equal(page.Tags, []string{"new-tag"}) {
		t.Fatalf("credential/tag index: %+v %v", page, err)
	}
	// A fresh scoped key must not search host names or read run summaries.
	key := APIKey{ID: "scoped", UserID: actor.ID, Name: "scoped", Purpose: "production", Scopes: []string{ScopeJobsRead}, CreatedAt: time.Now().Unix()}
	if err := db.CreateUserAPIKey(ctx, actor, key, "scoped-hash"); err != nil {
		t.Fatal(err)
	}
	scoped, err := db.ResolveAPIKey(ctx, "scoped-hash", time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := db.JobsPage(ctx, scoped, ListRequest{Limit: 1, Query: "Renamed"})
	if err != nil || jobs.Info.Matched != 0 {
		t.Fatalf("host name leaked via search: %+v %v", jobs, err)
	}
	if err := db.RevokeUserAPIKey(ctx, actor, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.JobsPage(ctx, scoped, ListRequest{Limit: 1}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked key: %v", err)
	}
	job.ExcludedHostIDs = []string{host.ID}
	job, err = db.SaveJob(ctx, actor, job, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	count("Renamed", 0)
	job.ExcludedHostIDs = nil
	job, err = db.SaveJob(ctx, actor, job, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteHost(ctx, actor, host.ID, host.Version); err != nil {
		t.Fatal(err)
	}
	count("Renamed", 0)
	if err := db.DeleteJob(ctx, actor, job.ID, job.Version); err != nil {
		t.Fatal(err)
	}
	pageJobs, err := db.JobsPage(ctx, actor, ListRequest{Limit: 1})
	if err != nil || pageJobs.Info.Total != 0 {
		t.Fatalf("deleted job metadata: %+v %v", pageJobs, err)
	}
}

func TestListBackfillIsAtomicAndPreservesRecords(t *testing.T) {
	db := openTest(t)
	actor := listActor(t, db)
	ctx := context.Background()
	if _, err := db.CreateCredential(ctx, actor, Credential{ID: "retained", Name: "\u00c9 retained", Algorithm: "ssh-ed25519", Fingerprint: "fingerprint"}, []byte("retained ciphertext")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, "DELETE FROM list_state"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, "DELETE FROM list_entries"); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := db.initializeLists(cancelled); err == nil {
		t.Fatal("cancelled backfill succeeded")
	}
	if err := db.initializeLists(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.initializeLists(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := db.CredentialsPage(ctx, actor, ListRequest{Limit: 1, Query: "\u00e9"})
	if err != nil || page.Info.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("backfill: %+v %v", page, err)
	}
	var encrypted []byte
	if err := db.db.QueryRowContext(ctx, "SELECT encrypted_secret FROM credentials WHERE id = 'retained'").Scan(&encrypted); err != nil || string(encrypted) != "retained ciphertext" {
		t.Fatal("backfill changed encrypted source data")
	}
}

func TestKeyPagesArePrivateAndCleanUpWithRevocation(t *testing.T) {
	db := openTest(t)
	actor := listActor(t, db)
	ctx := context.Background()
	other, err := db.CreateUser(ctx, actor, "other", "hash")
	if err != nil {
		t.Fatal(err)
	}
	otherActor := actorFor(t, db, other, "other-session")
	for _, owner := range []Actor{actor, otherActor} {
		key := APIKey{ID: owner.Username, Name: owner.Username, UserID: owner.ID, Purpose: "production", Scopes: []string{ScopeHostsRead}, CreatedAt: 1}
		if err := db.CreateUserAPIKey(ctx, owner, key, owner.Username+"-hash"); err != nil {
			t.Fatal(err)
		}
	}
	page, err := db.APIKeysPage(ctx, actor, ListRequest{Limit: 1})
	if err != nil || page.Info.Total != 1 || page.Items[0].UserID != actor.ID {
		t.Fatalf("owner filtering: %+v %v", page, err)
	}
	if err := db.ChangePassword(ctx, actor, other.ID, "new-hash"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM list_entries WHERE kind = 'keys' AND owner_id = ?", other.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("password change retained key metadata")
	}
}

func TestKeyCursorKeepsTimestampTiesAfterDeletion(t *testing.T) {
	db := openTest(t)
	actor := listActor(t, db)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if err := db.CreateUserAPIKey(ctx, actor, APIKey{ID: id, Name: "Same name", UserID: actor.ID, Purpose: "production", Scopes: []string{ScopeHostsRead}, CreatedAt: 1}, id+"-hash"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := db.APIKeysPage(ctx, actor, ListRequest{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != "a" || first.Info.NextCursor == "" {
		t.Fatalf("first tie: %+v %v", first, err)
	}
	if err := db.RevokeUserAPIKey(ctx, actor, "a"); err != nil {
		t.Fatal(err)
	}
	second, err := db.APIKeysPage(ctx, actor, ListRequest{Limit: 1, Cursor: first.Info.NextCursor})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != "b" {
		t.Fatalf("deleted cursor skipped tie: %+v %v", second, err)
	}
	third, err := db.APIKeysPage(ctx, actor, ListRequest{Limit: 1, Cursor: second.Info.NextCursor})
	if err != nil || len(third.Items) != 1 || third.Items[0].ID != "c" || third.Info.NextCursor != "" {
		t.Fatalf("final tie: %+v %v", third, err)
	}
}

func TestListCursorRejectsNULID(t *testing.T) {
	db := openTest(t)
	actor := listActor(t, db)
	encoded, err := json.Marshal(listCursor{Key: "valid\x00name", ID: "bad\x00id"})
	if err != nil {
		t.Fatal(err)
	}
	cursor := base64.RawURLEncoding.EncodeToString(encoded)
	if _, err := db.UsersPage(context.Background(), actor, ListRequest{Limit: 1, Cursor: cursor}); !errors.Is(err, ErrListRequest) {
		t.Fatalf("invalid cursor ID reached SQL: %v", err)
	}
}
