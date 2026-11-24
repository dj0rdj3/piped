package auth

import (
	"errors"
	"strings"
	"testing"
)

// Fixed before the refactor: catches parameter/format changes that a round-trip
// test alone would miss. The salt and password are public test fixtures.
const compatiblePasswordHash = "$argon2id$v=19$m=65536,t=3,p=1$MDEyMzQ1Njc4OWFiY2RlZg$KCWpWo6PJ5YL7uRFMJO7sLEFKagDXR8BXhjPvSXp2Mc"

func TestExistingPasswordHashesAndSharedWorkLimit(t *testing.T) {
	h := NewHasher()
	for range 2 {
		h.slots <- struct{}{}
	}
	if _, err := h.Verify(compatiblePasswordHash, "compatibility-password"); !errors.Is(err, ErrBusy) {
		t.Fatalf("verification bypassed the shared work limit: %v", err)
	}
	if _, err := h.Hash("compatibility-password"); !errors.Is(err, ErrBusy) {
		t.Fatalf("hashing bypassed the shared work limit: %v", err)
	}
	<-h.slots
	for _, password := range []string{"compatibility-password", "wrong-password"} {
		ok, err := h.Verify(compatiblePasswordHash, password)
		if err != nil || ok != (password == "compatibility-password") || len(h.slots) != 1 {
			t.Fatalf("compatibility or slot cleanup failed: match=%t error=%v slots=%d", ok, err, len(h.slots))
		}
	}
	if _, err := h.Hash("compatibility-password"); err != nil || len(h.slots) != 1 {
		t.Fatalf("hashing failed to release its slot: %v", err)
	}
	for _, malformed := range []string{"prefix" + compatiblePasswordHash, compatiblePasswordHash + "$extra"} {
		if ok, err := h.Verify(malformed, "compatibility-password"); ok || err == nil || len(h.slots) != 1 {
			t.Fatal("noncanonical hash accepted or leaked a work slot")
		}
	}
}

func TestPasswordHashes(t *testing.T) {
	h := NewHasher()
	password := "a long passphrase with spaces \U0001f511"
	a, err := h.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("password salts must be random")
	}
	for _, tc := range []struct {
		password string
		want     bool
	}{{password, true}, {"wrong password", false}, {strings.Repeat("x", 513), false}} {
		ok, err := h.Verify(a, tc.password)
		if err != nil || ok != tc.want {
			t.Fatalf("verification = %v, %v; want %v", ok, err, tc.want)
		}
	}
	for _, encoded := range []string{"", "$argon2id$v=19$m=999999999,t=3,p=1$bad$bad", strings.Replace(a, "v=19", "v=20", 1), strings.Replace(a, "$argon2id$", "$argon2i$", 1), a + "$extra", "$argon2id$v=19$m=65536,t=3,p=1$%%%%$%%%%"} {
		if _, err := h.Verify(encoded, password); err == nil {
			t.Errorf("accepted malformed hash %q", encoded)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	for _, password := range []string{"", strings.Repeat("x", 7), strings.Repeat("\u00e9", 7), strings.Repeat("x", 129), string([]byte{0xff})} {
		if !errors.Is(ValidatePassword(password), ErrPassword) {
			t.Error("accepted invalid password")
		}
	}
	for _, password := range []string{strings.Repeat("x", 8), strings.Repeat("\u00e9", 8), strings.Repeat("\u00e9", 128), "    long pass phrase    "} {
		if err := ValidatePassword(password); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUsernamePolicy(t *testing.T) {
	name, err := NormalizeUsername(" Admin.User ")
	if err != nil || name != "admin.user" {
		t.Fatalf("normalization: %q, %v", name, err)
	}
	for _, name := range []string{"", "ab", "-admin", "admin user", "\u7ba1\u7406\u5458", strings.Repeat("a", 65), "admin' OR 1=1--"} {
		if _, err := NormalizeUsername(name); !errors.Is(err, ErrUsername) {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestHasherBoundsConcurrency(t *testing.T) {
	h := NewHasher()
	h.slots <- struct{}{}
	h.slots <- struct{}{}
	if _, err := h.Hash("a sufficiently long password"); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v", err)
	}
}
