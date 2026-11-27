// Package apikey provides the security-sensitive primitives for issuing and
// verifying API keys: cryptographically-random key generation, hashing, and
// constant-time verification.
//
// This is the reusable, get-it-wrong-and-you're-breached part. Everything
// ELSE about API keys is application code you own: the api_keys table (add it
// with `forge scaffold entity`), the store that reads/writes it, and the
// auth.KeyValidator that turns a validated key into claims. The auth skill's
// "API keys" recipe shows the full wiring; the short version is:
//
//	// issuing a key (in your store's Create):
//	plaintext, err := apikey.Generate()          // "fk_..." — show once
//	prefix, _ := apikey.LookupPrefix(plaintext)   // indexed lookup handle
//	hash := apikey.Hash(plaintext)                // store this, never the key
//
//	// verifying (in your auth.KeyValidator.ValidateKey):
//	prefix, err := apikey.LookupPrefix(presented) // reject malformed early
//	row := store.findByPrefix(ctx, prefix)        // one indexed read
//	if !apikey.Verify(presented, row.Hash) { ...unauthenticated... }
//
// Keys are high-entropy random tokens (~190 bits), so a fast cryptographic
// hash (SHA-256) is the correct choice — the slow password hashes (bcrypt,
// argon2) exist to defend LOW-entropy secrets against brute force and buy
// nothing here while costing a lookup's worth of CPU per request.
//
// Keys start with forge's "fk_" unless the application names its own prefix
// with a [Format] (apikey.MustFormat("acme_")), whose methods replace
// Generate and LookupPrefix above; keys already issued as "fk_" keep working.
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

const (
	// KeyPrefix is prepended to every key [Generate] issues, and to every
	// key the zero [Format] issues. It makes keys recognizable in logs and
	// lets secret scanners match on "fk_". Every Format still accepts it.
	KeyPrefix = "fk_"

	// LookupPrefixLen is the number of post-KeyPrefix characters stored in
	// cleartext as an indexed lookup handle, so verification is a single
	// indexed row read rather than a full-table hash scan.
	LookupPrefixLen = 8

	// randomLen is the number of random base62 characters after KeyPrefix.
	// 62^32 ≈ 190 bits of entropy.
	randomLen = 32

	base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// Generate returns a new random API key of the form "fk_<32 base62 chars>".
// The plaintext is returned once; store only its Hash and LookupPrefix.
// A product that issues keys under its own prefix uses [Format.Generate].
func Generate() (string, error) {
	return generate(KeyPrefix)
}

func generate(prefix string) (string, error) {
	alphabetLen := big.NewInt(int64(len(base62Alphabet)))
	var sb strings.Builder
	sb.Grow(len(prefix) + randomLen)
	sb.WriteString(prefix)
	for i := 0; i < randomLen; i++ {
		n, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			return "", fmt.Errorf("apikey: crypto/rand: %w", err)
		}
		sb.WriteByte(base62Alphabet[n.Int64()])
	}
	return sb.String(), nil
}

// Hash returns the hex-encoded SHA-256 of key. Store this, never the key.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// LookupPrefix returns the indexed lookup handle for key (the LookupPrefixLen
// characters following KeyPrefix). It doubles as a cheap format check:
// malformed input is rejected before any DB work. A product that issues keys
// under its own prefix uses [Format.LookupPrefix].
func LookupPrefix(key string) (string, error) {
	return lookupPrefix(key, KeyPrefix)
}

func lookupPrefix(key, prefix string) (string, error) {
	if prefix == "" || !strings.HasPrefix(key, prefix) || len(key) < len(prefix)+LookupPrefixLen {
		return "", fmt.Errorf("apikey: malformed key")
	}
	return key[len(prefix) : len(prefix)+LookupPrefixLen], nil
}

// Verify reports whether key hashes to storedHash, using a constant-time
// comparison so verification time does not leak how much of the hash matched.
func Verify(key, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(Hash(key)), []byte(storedHash)) == 1
}
