// Package secrets provides authenticated encryption for stored application secrets.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

var ErrDecrypt = errors.New("cannot decrypt stored secret: wrong key or damaged data")

type Vault struct{ aead cipher.AEAD }

func New(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("encryption key must contain exactly 32 random bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead}, nil
}

// Seal binds ciphertext to its purpose and record. Never reuse a purpose across
// different fields. The version and random nonce precede the authenticated data.
func (v *Vault) Seal(purpose string, plaintext []byte) []byte {
	nonce := make([]byte, v.aead.NonceSize())
	_, _ = rand.Read(nonce)
	header := append([]byte{1}, nonce...)
	return v.aead.Seal(header, nonce, plaintext, []byte("piped:v1:"+purpose))
}

func (v *Vault) Open(purpose string, ciphertext []byte) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(ciphertext) < 1+n+v.aead.Overhead() || ciphertext[0] != 1 {
		return nil, ErrDecrypt
	}
	plain, err := v.aead.Open(nil, ciphertext[1:1+n], ciphertext[1+n:], []byte("piped:v1:"+purpose))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}
