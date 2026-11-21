package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// IDPrefix starts every session id. A credential with this prefix is a
// session id, never a JWT (whose base64url header begins "eyJ") or a forge API
// key ("fk_"), so an authenticator can route it by shape.
const IDPrefix = "fs_"

// idBytes is the entropy of a session id: 256 bits, so the id is both an
// unguessable bearer secret and strong enough to derive keys from directly.
const idBytes = 32

// idLen is the length of a well-formed id: the prefix and 32 bytes of
// unpadded base64url.
var idLen = len(IDPrefix) + base64.RawURLEncoding.EncodedLen(idBytes)

// NewID mints a session id: IDPrefix followed by 32 random bytes in unpadded
// base64url. It is the cookie's value and the session's only secret.
func NewID() (string, error) {
	raw := make([]byte, idBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("session: generate id: %w", err)
	}
	return IDPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// IsID reports whether s has the shape of a session id. It says nothing about
// whether such a session exists.
func IsID(s string) bool {
	if len(s) != idLen || !strings.HasPrefix(s, IDPrefix) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s[len(IDPrefix):])
	return err == nil && len(raw) == idBytes
}

// keys are the two keys derived from a session id. lookup keys the store's
// row; seal encrypts the row's tokens. Neither reveals the id or the other.
type keys struct {
	lookup []byte
	seal   []byte
}

// HKDF info strings: one purpose each, versioned so a future derivation can
// coexist with this one.
const (
	lookupInfo = "forge/pkg/session lookup v1"
	sealInfo   = "forge/pkg/session seal v1"
)

// deriveKeys derives the lookup and sealing keys from a well-formed id. The
// id carries 256 bits of entropy, so HKDF needs no salt to be a sound KDF
// here (RFC 5869 §3.1).
func deriveKeys(id string) (keys, error) {
	if !IsID(id) {
		return keys{}, ErrNoSession
	}
	secret := []byte(id)
	lookup, err := hkdf.Key(sha256.New, secret, nil, lookupInfo, 32)
	if err != nil {
		return keys{}, err
	}
	seal, err := hkdf.Key(sha256.New, secret, nil, sealInfo, 32)
	if err != nil {
		return keys{}, err
	}
	return keys{lookup: lookup, seal: seal}, nil
}

// Handle names a session WITHOUT being a credential: the hex of its lookup
// key. An account page lists sessions by handle and revokes one by handle
// ([Manager.RevokeHandle]); knowing a handle does not let anyone use the
// session, because the id cannot be recovered from it.
func handleOf(lookup []byte) string { return hex.EncodeToString(lookup) }

// Tokens are what an identity provider issued for one session.
//
// They are credentials. Tokens redacts itself under fmt and log/slog, so a
// stray log line does not leak one; take the fields out deliberately.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	// Expiry is when AccessToken expires. The zero time is treated as
	// "already due for a refresh".
	Expiry time.Time
}

const redacted = "[REDACTED]"

// String implements fmt.Stringer and redacts every token.
func (Tokens) String() string { return "session.Tokens{" + redacted + "}" }

// GoString implements fmt.GoStringer so %#v does not reflect through.
func (Tokens) GoString() string { return "session.Tokens{" + redacted + "}" }

// LogValue implements slog.LogValuer and redacts every token.
func (Tokens) LogValue() slog.Value { return slog.StringValue(redacted) }

// sealedTokens is the plaintext a row's sealed column holds.
type sealedTokens struct {
	Access  string `json:"a"`
	Refresh string `json:"r,omitempty"`
	ID      string `json:"i,omitempty"`
	Expiry  int64  `json:"e"` // unix milliseconds; 0 for the zero time
}

// sealVersion is the first byte of every sealed blob.
const sealVersion byte = 1

// errCorrupt reports a row whose tokens cannot be opened with the id that
// named it: written by another derivation, or tampered with.
var errCorrupt = errors.New("session: the stored tokens cannot be opened")

// seal encrypts t under k.seal, with the lookup key as additional data so a
// sealed blob moved onto another row does not open there.
func seal(k keys, t Tokens) ([]byte, error) {
	var exp int64
	if !t.Expiry.IsZero() {
		exp = t.Expiry.UnixMilli()
	}
	plain, err := json.Marshal(sealedTokens{Access: t.AccessToken, Refresh: t.RefreshToken, ID: t.IDToken, Expiry: exp})
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(k.seal)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("session: generate nonce: %w", err)
	}
	out := make([]byte, 0, 1+len(nonce)+len(plain)+gcm.Overhead())
	out = append(out, sealVersion)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plain, k.lookup), nil
}

// open reverses seal.
func open(k keys, blob []byte) (Tokens, error) {
	gcm, err := newGCM(k.seal)
	if err != nil {
		return Tokens{}, err
	}
	if len(blob) < 1+gcm.NonceSize() || blob[0] != sealVersion {
		return Tokens{}, errCorrupt
	}
	nonce := blob[1 : 1+gcm.NonceSize()]
	plain, err := gcm.Open(nil, nonce, blob[1+gcm.NonceSize():], k.lookup)
	if err != nil {
		return Tokens{}, errCorrupt
	}
	var s sealedTokens
	if err := json.Unmarshal(plain, &s); err != nil {
		return Tokens{}, errCorrupt
	}
	t := Tokens{AccessToken: s.Access, RefreshToken: s.Refresh, IDToken: s.ID}
	if s.Expiry != 0 {
		t.Expiry = time.UnixMilli(s.Expiry)
	}
	return t, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
