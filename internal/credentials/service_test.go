package credentials

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"piped/internal/secrets"
	"piped/internal/store"
)

func fixture(t *testing.T) (*Service, store.Actor, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "piped.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	u, err := db.InitAdmin(context.Background(), "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(context.Background(), u, "session", time.Now().Unix(), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	vault, _ := secrets.New(bytes.Repeat([]byte{17}, 32))
	s, err := New(context.Background(), db, vault)
	if err != nil {
		t.Fatal(err)
	}
	return s, store.Actor{User: u, SessionHash: "session"}, path
}

func encodedKey(t *testing.T, raw any, passphrase string) string {
	t.Helper()
	var block *pem.Block
	var err error
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(raw, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(raw, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block))
}

func TestGenerateImportAndEncryptedPersistence(t *testing.T) {
	s, actor, path := fixture(t)
	ctx := context.Background()
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	ecdsaKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(ed)
	if err != nil {
		t.Fatal(err)
	}
	// Legacy PEM is accepted for existing keys; new keys always use OpenSSH.
	legacy, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rsaKey), []byte("legacy-import-passphrase"), x509.PEMCipherAES256)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, mode, private, passphrase, algorithm string }{
		{"Generated", "generate", "", "", "ssh-ed25519"},
		{"Ed import", "import", encodedKey(t, ed, ""), "", "ssh-ed25519"},
		{"Encrypted import", "import", encodedKey(t, ed, "sensitive-passphrase-123"), "sensitive-passphrase-123", "ssh-ed25519"},
		{"ECDSA", "import", encodedKey(t, ecdsaKey, ""), "", "ecdsa-sha2-nistp256"},
		{"RSA", "import", encodedKey(t, rsaKey, ""), "", "ssh-rsa"},
		{"PKCS8", "import", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})), "", "ssh-ed25519"},
		{"Encrypted PEM", "import", string(pem.EncodeToMemory(legacy)), "legacy-import-passphrase", "ssh-rsa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := s.Create(ctx, actor, CreateInput{Name: "  " + tc.name + "  ", Mode: tc.mode, PrivateKey: tc.private, Passphrase: tc.passphrase})
			if err != nil {
				t.Fatal(err)
			}
			if c.Name != tc.name || c.Algorithm != tc.algorithm || c.HasPassphrase != (tc.passphrase != "") {
				t.Fatalf("metadata: %+v", c)
			}
			pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(c.PublicKey))
			if err != nil || ssh.FingerprintSHA256(pub) != c.Fingerprint {
				t.Fatal("incorrect public key/fingerprint")
			}
			if comment != "admin@"+s.hostname {
				t.Fatalf("missing app user/system hostname comment: %q", comment)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var ciphertext []byte
			if err := db.QueryRow("SELECT encrypted_secret FROM credentials WHERE id = ?", c.ID).Scan(&ciphertext); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(ciphertext, []byte("PRIVATE KEY")) || bytes.Contains(ciphertext, []byte("sensitive-passphrase-123")) {
				t.Fatal("plaintext stored")
			}
			plain, err := s.vault.Open("credential:"+c.ID+":secret", ciphertext)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(plain)
			var secret struct {
				PrivateKey string `json:"private_key"`
				Passphrase string `json:"passphrase"`
			}
			if err := json.Unmarshal(plain, &secret); err != nil {
				t.Fatal(err)
			}
			if secret.Passphrase != tc.passphrase || (tc.private != "" && secret.PrivateKey != tc.private) {
				t.Fatal("stored secret differs")
			}
			var signer ssh.Signer
			if tc.passphrase != "" {
				signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(secret.PrivateKey), []byte(secret.Passphrase))
			} else {
				signer, err = ssh.ParsePrivateKey([]byte(secret.PrivateKey))
			}
			if err != nil || !bytes.Equal(signer.PublicKey().Marshal(), pub.Marshal()) {
				t.Fatal("stored key cannot be used")
			}
			if _, err := s.vault.Open("credential:other:secret", ciphertext); !errors.Is(err, secrets.ErrDecrypt) {
				t.Fatal("record substitution accepted")
			}
			public, _ := json.Marshal(c)
			if bytes.Contains(public, []byte("private_key")) || bytes.Contains(public, []byte("sensitive-passphrase-123")) {
				t.Fatal("public metadata contains secret")
			}
		})
	}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("PRIVATE KEY")) || bytes.Contains(data, []byte("sensitive-passphrase-123")) {
			t.Fatal("secret appears in database/WAL")
		}
	}
	if _, err := New(ctx, s.store, s.vault); err != nil {
		t.Fatal("same-key restart failed", err)
	}
	wrong, _ := secrets.New(make([]byte, 32))
	if _, err := New(ctx, s.store, wrong); !errors.Is(err, secrets.ErrDecrypt) {
		t.Fatal("wrong key restart accepted")
	}
}

func TestImportedPublicKeyValidationAndComments(t *testing.T) {
	s, actor, _ := fixture(t)
	s.hostname = "piped-host"
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	private := encodedKey(t, key, "")
	for i, tc := range []struct{ supplied, expected string }{
		{public, public},
		{public + " old-comment@other-host", public + " old-comment@other-host"},
		{public + " deployment key with spaces", public + " deployment key with spaces"},
		{public + "\n", public},
	} {
		c, err := s.Create(context.Background(), actor, CreateInput{Name: fmt.Sprintf("Matched %d", i), Mode: "import", PrivateKey: private, PublicKey: tc.supplied})
		if err != nil {
			t.Fatal(err)
		}
		if c.PublicKey != tc.expected {
			t.Fatalf("unexpected comment/key: %q", c.PublicKey)
		}
		items, err := s.List(context.Background(), actor)
		if err != nil {
			t.Fatal(err)
		}
		for _, stored := range items {
			if stored.ID == c.ID && stored.PublicKey != tc.expected {
				t.Fatal("listing changed the original comment")
			}
		}
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	for _, supplied := range []string{
		string(ssh.MarshalAuthorizedKey(otherSigner.PublicKey())), "not a public key", public + "\n" + public, "junk\n" + public,
		`command="echo unsafe" ` + public, strings.Repeat("a", 8193), public + " bad\x00comment",
	} {
		_, err := s.Create(context.Background(), actor, CreateInput{Name: "Invalid", Mode: "import", PrivateKey: private, PublicKey: supplied})
		if !errors.Is(err, ErrInput) {
			t.Fatalf("invalid public key accepted: %v", err)
		}
	}
	if _, err := s.Create(context.Background(), actor, CreateInput{Name: "Generated", Mode: "generate", PublicKey: public}); !errors.Is(err, ErrInput) {
		t.Fatal("generation accepted external public key")
	}
	// Listing must preserve absent comments, too.
	legacy, err := s.store.CreateCredential(context.Background(), actor, store.Credential{ID: "legacy", Name: "Legacy", PublicKey: public}, []byte("encrypted"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.List(context.Background(), actor)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range items {
		if c.ID == legacy.ID && c.PublicKey != public {
			t.Fatal("listing added a comment")
		}
	}
	s.hostname = "unusual\nhost\tname"
	if s.comment(actor) != "admin@unusual_host_name" {
		t.Fatal("comment permits line injection")
	}
}

func TestInvalidKeysAndInputs(t *testing.T) {
	s, actor, _ := fixture(t)
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	plain := encodedKey(t, key, "")
	encrypted := encodedKey(t, key, "correct passphrase")
	bad := append(ed25519.PrivateKey(nil), key...)
	bad[len(bad)-1] ^= 1
	for _, input := range []CreateInput{
		{Name: "", Mode: "generate"}, {Name: strings.Repeat("a", 101), Mode: "generate"}, {Name: "a\x00b", Mode: "generate"},
		{Name: "Name", Mode: "password"}, {Name: "Name", Mode: "generate", PrivateKey: plain}, {Name: "Name", Mode: "generate", Passphrase: "unused"},
		{Name: "Name", Mode: "import"}, {Name: "Name", Mode: "import", PrivateKey: "not a key"}, {Name: "Name", Mode: "import", PrivateKey: strings.Repeat("a", 32769)},
		{Name: "Name", Mode: "import", PrivateKey: plain, Passphrase: strings.Repeat("a", 1025)},
		{Name: "Name", Mode: "import", PrivateKey: plain, Passphrase: "unused"}, {Name: "Name", Mode: "import", PrivateKey: encrypted},
		{Name: "Name", Mode: "import", PrivateKey: encrypted, Passphrase: "wrong"}, {Name: "Name", Mode: "import", PrivateKey: plain + plain},
		{Name: "Name", Mode: "import", PrivateKey: "junk\n" + plain}, {Name: "Name", Mode: "import", PrivateKey: encodedKey(t, bad, "")},
	} {
		if _, err := s.Create(context.Background(), actor, input); !errors.Is(err, ErrInput) {
			t.Fatalf("invalid input accepted or unexpected error: %v", err)
		}
	}
	items, err := s.List(context.Background(), actor)
	if err != nil || len(items) != 0 {
		t.Fatal("invalid requests wrote credentials")
	}
	// Alter only the KDF envelope to assert that huge work factors are rejected
	// before the library performs the expensive password derivation.
	block, _ := pem.Decode([]byte(encrypted))
	const magic = "openssh-key-v1\x00"
	var envelope struct {
		Cipher  string
		KDF     string
		Options string
		Keys    uint32
		Rest    []byte `ssh:"rest"`
	}
	if err := ssh.Unmarshal(block.Bytes[len(magic):], &envelope); err != nil {
		t.Fatal(err)
	}
	for _, rounds := range []uint32{0, 65, ^uint32(0)} {
		envelope.Options = string(ssh.Marshal(struct {
			Salt   string
			Rounds uint32
		}{"salt", rounds}))
		block.Bytes = append([]byte(magic), ssh.Marshal(envelope)...)
		if err := checkPEM(pem.EncodeToMemory(block)); !errors.Is(err, ErrInput) {
			t.Fatalf("accepted KDF rounds %d", rounds)
		}
	}
	weak := &rsa.PrivateKey{PublicKey: rsa.PublicKey{N: big.NewInt(17), E: 65537}}
	if err := validateKey(weak); !errors.Is(err, ErrInput) {
		t.Fatal("weak RSA accepted")
	}
}

func TestBusyAndRevokedSession(t *testing.T) {
	s, actor, _ := fixture(t)
	input := CreateInput{Name: "Name", Mode: "generate"}
	s.workers <- struct{}{}
	s.workers <- struct{}{}
	if _, err := s.Create(context.Background(), actor, input); !errors.Is(err, ErrBusy) {
		t.Fatal("unbounded key processing")
	}
	<-s.workers
	<-s.workers
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Create(ctx, actor, input); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled work accepted")
	}
	if err := s.store.DeleteSession(context.Background(), actor.SessionHash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(context.Background(), actor, input); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatal("revoked session created credential")
	}
}

func TestCredentialNameUnicodeBoundariesAndControls(t *testing.T) {
	for _, name := range []string{"Name", strings.Repeat("a", 100), strings.Repeat("\u65e5", 100)} {
		normalized, err := Name(" \t" + name + "\r\n")
		if err != nil || normalized != name {
			t.Fatalf("valid credential name changed: %v", err)
		}
	}
	for _, name := range []string{"", " \t\r\n", strings.Repeat("a", 101), strings.Repeat("\u65e5", 101), "a\x00b", "a\nb", "a\tb", "a\u0085b", "\xff"} {
		if _, err := Name(name); !errors.Is(err, ErrInput) {
			t.Fatalf("invalid credential name accepted: %q", name)
		}
	}
}
