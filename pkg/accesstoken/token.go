// Package accesstoken is the security primitive behind Reliant Labs' ONE
// machine credential, `rlat_`: its wire format, generation, hashing,
// constant-time comparison, the closed scope vocabulary, the rule that a
// token may never grant authority it does not hold, and the Principal a
// presented token resolves to.
//
// ── WHAT IS HERE, AND WHAT IS NOT ─────────────────────────────────────
//
// This package follows forge/pkg/apikey's split exactly: it holds the
// reusable, get-it-wrong-and-you're-breached part, and nothing else. It has
// no database, no SQL and no store. The TABLE and the STORE that reads and
// writes it are application code, owned by whichever deployment issues tokens:
//
//   - control-plane owns controlplane.access_tokens for the hosted product;
//   - a self-hosted reliant with no control-plane owns its own table.
//
// Both import THIS package, so the parts that decide whether a credential is
// safe — entropy, hashing, comparison, scope parsing, the grant rule and the
// resource binding — have one implementation however many deployments there
// are.
//
// ── WHY NOT pkg/apikey ────────────────────────────────────────────────
//
// pkg/apikey is the generic `fk_` key a scaffolded forge app owns: a bearer
// with no authority model beyond "this key exists". An access token carries a
// closed scope set, an org, an optional acting user and an optional resource
// binding, and the grant rule is written against all of that. Folding that
// into apikey would make every forge app's key speak this vocabulary.
//
// ── THE PREFIX ────────────────────────────────────────────────────────
//
// Tokens are `rlat_<43 base62 chars>`. ONE prefix for every machine credential
// Reliant issues; SCOPES say what a given credential may do. The families this
// replaced (`rlnt_pat_`, `rlnt_conn_`, `rlnt_` LLM keys, `dpat_`) shared or
// nearly shared a namespace, so a credential presented to the wrong subsystem
// matched that subsystem's prefix test and failed at a hash lookup instead of
// at the boundary. One prefix removes that whole class of bug.
package accesstoken

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// Prefix marks a Reliant machine credential.
	Prefix = "rlat_"

	// randomBytes is the entropy behind each token: 32 bytes of crypto/rand,
	// one base62 character per byte. That is far beyond any guessing attack,
	// which is also what justifies SHA-256 over a slow KDF (see Hash).
	randomBytes = 32

	// TokenLen is the exact length of every token this package mints.
	TokenLen = len(Prefix) + randomBytes

	// DisplayPrefixLen is how many characters of the random suffix are kept,
	// alongside Prefix, as a safe-to-show identifier. Eight base62 chars
	// (~47 bits) tell an org's tokens apart without narrowing the secret by
	// anything that matters.
	DisplayPrefixLen = 8
)

// base62Chars is the token alphabet. Alphanumeric only, so a token pasted into
// a CI config, a shell export or an HTTP header never needs quoting.
const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Minted is a freshly generated token: the plaintext to return exactly once,
// and the two values persisted in its place. A struct rather than positional
// results because the fields have opposite handling rules — Plaintext is
// returned and never stored or logged; Hash is stored and never returned.
type Minted struct {
	// Plaintext is the secret. Never log it; never put it on a list response.
	Plaintext string
	// Hash is the SHA-256 hex digest — the only persisted form of the secret.
	Hash string
	// DisplayPrefix ("rlat_A3f9Kd2p") is safe to store, log and display.
	DisplayPrefix string
}

// Mint generates a new token.
//
// The suffix indexes base62Chars with `b % 62`, which is very slightly
// non-uniform (256 is not a multiple of 62). The bias costs well under one bit
// across the whole suffix, against ~250 bits of entropy.
func Mint() (Minted, error) {
	buf := make([]byte, randomBytes)
	if _, err := rand.Read(buf); err != nil {
		// Never work around an unreadable CSPRNG: the fallback would be a
		// guessable credential.
		return Minted{}, fmt.Errorf("accesstoken: generating token entropy: %w", err)
	}

	var sb strings.Builder
	sb.Grow(TokenLen)
	sb.WriteString(Prefix)
	for _, b := range buf {
		sb.WriteByte(base62Chars[int(b)%len(base62Chars)])
	}
	plaintext := sb.String()

	return Minted{
		Plaintext:     plaintext,
		Hash:          Hash(plaintext),
		DisplayPrefix: plaintext[:len(Prefix)+DisplayPrefixLen],
	}, nil
}

// Hash returns the SHA-256 hex digest stored for a plaintext token.
//
// Unsalted and deterministic on purpose: the digest IS the lookup key, so
// authentication is one indexed read. Salting defends a small input space
// against precomputation; 32 random bytes have no such space. The prefix is
// part of the hashed input.
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// HasFormat reports whether a bearer token has the exact shape Mint produces.
//
// A CHEAP PRE-FILTER, never an authorization decision: it lets a validator
// chain skip a lookup for the overwhelming majority of bearers (JWTs). It
// tests the exact length and the alphabet, not just the prefix, so a
// truncated, padded or foreign token is refused on shape.
func HasFormat(token string) bool {
	if len(token) != TokenLen || !strings.HasPrefix(token, Prefix) {
		return false
	}
	for i := len(Prefix); i < len(token); i++ {
		if strings.IndexByte(base62Chars, token[i]) < 0 {
			return false
		}
	}
	return true
}

// EqualHash reports whether two token hashes are equal, in constant time.
//
// The stored digest is unsalted and the hash function is public, so a `==`
// that returns at the first differing byte would let an attacker extract the
// digest byte by byte and then search for a preimage offline. Constant time
// removes that oracle. Unequal LENGTHS return early, which is harmless: a
// SHA-256 hex digest's length is a public constant.
func EqualHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
