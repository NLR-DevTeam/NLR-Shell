//go:build !windows

package store

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
)

// Secrets are sealed with XChaCha20-Poly1305 under a random key kept beside
// the config file with owner-only permissions. There is no OS keyring
// dependency: whoever can read the key file can read the secrets, which is
// the strongest guarantee a plain user directory can give.
const keyFile = "secret.key"

var (
	keyMu  sync.Mutex
	keyVal []byte
)

// secretKey returns the local encryption key, creating it on first use.
func secretKey() ([]byte, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if keyVal != nil {
		return keyVal, nil
	}
	p := filepath.Join(Dir(), keyFile)
	if b, err := os.ReadFile(p); err == nil && len(b) == chacha20poly1305.KeySize {
		keyVal = b
		return keyVal, nil
	}
	k := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	// Create the file exclusively so a concurrent writer cannot be
	// clobbered by a half-written key.
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if b, rerr := os.ReadFile(p); rerr == nil && len(b) == chacha20poly1305.KeySize {
			keyVal = b
			return keyVal, nil
		}
		return nil, err
	}
	_, werr := f.Write(k)
	if err := f.Close(); err != nil && werr == nil {
		werr = err
	}
	if werr != nil {
		os.Remove(p)
		return nil, werr
	}
	keyVal = k
	return keyVal, nil
}

// Encrypt protects s with the local key. It returns an empty string for
// empty input or on failure.
func Encrypt(s string) string {
	if s == "" {
		return ""
	}
	key, err := secretKey()
	if err != nil {
		return ""
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return ""
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(s), nil))
}

// Decrypt reverses Encrypt. It returns an empty string on failure.
func Decrypt(s string) string {
	if s == "" {
		return ""
	}
	in, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(in) <= chacha20poly1305.NonceSize {
		return ""
	}
	key, err := secretKey()
	if err != nil {
		return ""
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return ""
	}
	out, err := aead.Open(nil, in[:aead.NonceSize()], in[aead.NonceSize():], nil)
	if err != nil {
		return ""
	}
	return string(out)
}
