package devidp

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Sealed binding cookies.
//
// A sign-in ceremony that spans two requests (passkey begin → finish, magic
// request → consume, social start → callback, invite → passkey enrolment)
// binds its state to the browser that started it with an HttpOnly cookie.
// The cookie is SEALED (AES-GCM) for two reasons:
//
//   - CONFIDENTIALITY FROM THE REQUESTER. Whoever asks for a magic link or a
//     passkey challenge receives the cookie, and can read it. Its contents
//     differ between "that address has an account" and "it does not" (a
//     real session versus a decoy), so in clear text it would be the
//     enumeration oracle the uniform responses exist to close.
//   - INTEGRITY. A forged cookie naming another user is refused before the
//     issuer is called, rather than relying only on the issuer's own check.
//
// AES-GCM keeps the plaintext's length, so the plaintext is PADDED to a fixed
// size (sealedPlainSize) first: otherwise the cookie's length alone says which
// kind of binding it holds — a real challenge session against a decoy, a
// one-time-code session against "nothing to send" — which is the enumeration
// oracle again, read from the Set-Cookie header.
//
// The key is derived from the broker's own token, which every replica shares
// and nobody else holds, so no new secret has to be provisioned. Rotating the
// token invalidates cookies in flight; their life is minutes, and the person
// starts the ceremony again.

const sealInfo = "forge devidp binding cookie v1"

// sealer seals and opens binding-cookie payloads.
type sealer struct {
	key func() []byte // the current key material (the broker token)
	now func() time.Time

	mu    sync.Mutex
	cache map[[32]byte]cipher.AEAD
}

type sealedEnvelope struct {
	Kind    string          `json:"k"`
	Expires int64           `json:"e"`
	Payload json.RawMessage `json:"p"`
}

var errSealed = errors.New("binding cookie is missing, expired or not ours")

// marshalPlain is json.Marshal without HTML escaping and without the
// trailing newline an Encoder adds.
func marshalPlain(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// errPayloadTooLarge: the payload does not fit a binding cookie.
var errPayloadTooLarge = errors.New("binding cookie payload is too large")

// sealedPlainSize is the padded plaintext size: every binding cookie of every
// kind and state seals to the same length. A payload that does not fit is an
// error, never a longer cookie.
const sealedPlainSize = 512

// pad frames plain as [2-byte length][plain][zeros] of sealedPlainSize bytes.
func pad(plain []byte) ([]byte, error) {
	if len(plain) > sealedPlainSize-2 {
		return nil, fmt.Errorf("%w: %d bytes, more than %d", errPayloadTooLarge, len(plain), sealedPlainSize-2)
	}
	out := make([]byte, sealedPlainSize)
	binary.BigEndian.PutUint16(out, uint16(len(plain)))
	copy(out[2:], plain)
	return out, nil
}

func unpad(padded []byte) ([]byte, bool) {
	if len(padded) != sealedPlainSize {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(padded))
	if n > sealedPlainSize-2 {
		return nil, false
	}
	return padded[2 : 2+n], true
}

func (s *sealer) aead() (cipher.AEAD, error) {
	material := s.key()
	if len(material) == 0 {
		return nil, fmt.Errorf("no key material for binding cookies (the broker has no token)")
	}
	k := sha256.Sum256(append([]byte(sealInfo+"\x00"), material...))
	s.mu.Lock()
	defer s.mu.Unlock()
	if a, ok := s.cache[k]; ok {
		return a, nil
	}
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if s.cache == nil || len(s.cache) > 4 {
		s.cache = map[[32]byte]cipher.AEAD{}
	}
	s.cache[k] = a
	return a, nil
}

// seal encodes payload under kind, valid for ttl. The kind is bound into the
// envelope so a cookie of one ceremony cannot be replayed as another's.
func (s *sealer) seal(kind string, payload any, ttl time.Duration) (string, error) {
	a, err := s.aead()
	if err != nil {
		return "", err
	}
	// Not json.Marshal: it escapes <, > and & to six bytes each, so a
	// payload's sealed size would depend on its characters (a password with
	// HTML in it), not just its length.
	p, err := marshalPlain(payload)
	if err != nil {
		return "", err
	}
	env, err := marshalPlain(sealedEnvelope{Kind: kind, Expires: s.now().Add(ttl).UnixMilli(), Payload: p})
	if err != nil {
		return "", err
	}
	plain, err := pad(env)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(a.Seal(nonce, nonce, plain, []byte(kind))), nil
}

// open decodes a sealed value of kind into out.
func (s *sealer) open(kind, value string, out any) error {
	if value == "" {
		return errSealed
	}
	a, err := s.aead()
	if err != nil {
		return err
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < a.NonceSize() {
		return errSealed
	}
	padded, err := a.Open(nil, raw[:a.NonceSize()], raw[a.NonceSize():], []byte(kind))
	if err != nil {
		return errSealed
	}
	plain, ok := unpad(padded)
	if !ok {
		return errSealed
	}
	var env sealedEnvelope
	if err := json.Unmarshal(plain, &env); err != nil || env.Kind != kind || s.now().UnixMilli() > env.Expires {
		return errSealed
	}
	if err := json.Unmarshal(env.Payload, out); err != nil {
		return errSealed
	}
	return nil
}
