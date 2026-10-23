// Package crypto provides at-rest AES-256-GCM encryption under a VERSIONED
// KEYRING, so a key can be rotated without making every already-sealed value
// permanently unreadable.
//
// The wire format is the "CPK1" envelope, byte-for-byte compatible with the
// one control-plane has shipped; blobs sealed by either implementation open
// with the other. See envelope.go for the layout and the reasoning for it.
//
// There is no process-global state and no hard-coded environment variable:
// build a *Keyring with ParseKeyring or KeyringFromEnv and pass it to whoever
// seals or opens.
//
// # Rotation
//
// The keyring is an ordered list; the FIRST entry is the primary and the only
// key that seals, while every entry can open. Rotating is one prepend:
//
//	before: v1:AAA...
//	after:  v2:BBB...,v1:AAA...
//
// New writes use v2, old rows still open under v1. Keep v1 until a backfill
// has re-sealed every row (EnvelopePrefixForKeyID builds the SQL prefix for
// "which rows still use v1"), then delete it. At no point is a key referenced
// that is not present, so neither step has an ordering hazard.
//
//forge:exclude-contract: pkg/ library: AES-GCM helpers over the standard library
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Sentinels are distinguishable with errors.Is. An operator who rotates a key
// must be told what they did, not shown a bare authentication failure.
var (
	// ErrKeyNotConfigured: the keyring source (env var) is unset or blank.
	// Whether that is fatal is the caller's call; an absent keyring is not
	// the same as a malformed one.
	ErrKeyNotConfigured = errors.New("encryption keyring not configured")

	// ErrKeyringInvalid: present but malformed — bad entry, bad key id, key
	// not 32 bytes, duplicate id, or a corrupt envelope header. Fail at boot.
	ErrKeyringInvalid = errors.New("not a valid encryption keyring")

	// ErrUnknownKeyID: a well-formed blob naming a key the keyring does not
	// hold. This means "put the key back", not "someone tampered with it".
	ErrUnknownKeyID = errors.New("ciphertext was sealed under a key id that is not in the keyring")

	// ErrUnversionedCiphertext: no envelope header, i.e. the pre-keyring raw
	// nonce ‖ ciphertext format. Refused rather than opened under an
	// implicit "v1"; see decodeEnvelope.
	ErrUnversionedCiphertext = errors.New("ciphertext predates the versioned envelope and cannot be opened")
)

const (
	// maxKeyIDLen bounds the id so the envelope's single length byte always
	// suffices and a corrupt header cannot request an implausible span.
	maxKeyIDLen = 32
	aesKeyLen   = 32
	// defaultKeyID names the key of a one-entry keyring written as a bare
	// base64 key with no "id:" prefix.
	defaultKeyID = "v1"
)

// keyIDForm is the only shape a key id may take. The id lands in ciphertext
// headers, logs and SQL predicates; lowercase alphanumerics plus '_' and '-'
// means no quoting question and no clash with the ',' and ':' that separate
// keyring entries.
var keyIDForm = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Keyring is a set of AES-256 keys: every one can open, exactly one (the
// primary) seals. Safe for concurrent use after construction.
type Keyring struct {
	primaryID string
	byID      map[string]cipher.AEAD
	ids       []string // declaration order, primary first

	// readNonce fills the GCM nonce. Tests replace it to produce golden
	// fixtures; production always uses crypto/rand.
	readNonce func([]byte) error
}

func randomNonce(b []byte) error {
	_, err := io.ReadFull(rand.Reader, b)
	return err
}

// ParseKeyring reads the keyring grammar:
//
//	keyring = entry [ "," entry ]*
//	entry   = key-id ":" base64(32 bytes)
//	        | base64(32 bytes)          -- single entry only; id becomes "v1"
//
// The first entry is the primary. Position rather than a second "primary=" name
// was chosen because two settings can disagree in the worst way: a primary
// naming a key absent from the keyring boots fine and opens every old row, then
// fails on the first write. With position, you cannot prepend a key without
// adding it. Whitespace around entries and ids is tolerated.
func ParseKeyring(raw string) (*Keyring, error) {
	entries := strings.Split(raw, ",")
	kr := &Keyring{byID: make(map[string]cipher.AEAD, len(entries)), readNonce: randomNonce}

	for i, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("%w: entry %d is empty (trailing or doubled comma?)", ErrKeyringInvalid, i+1)
		}

		id, encoded := defaultKeyID, entry
		if idx := strings.Index(entry, ":"); idx >= 0 {
			id, encoded = strings.TrimSpace(entry[:idx]), strings.TrimSpace(entry[idx+1:])
		} else if len(entries) > 1 {
			// A bare key in a list could collide with an explicit v1 or
			// silently become the primary.
			return nil, fmt.Errorf("%w: entry %d has no %q key-id prefix; only a single-entry keyring may omit the id (it is then %q)",
				ErrKeyringInvalid, i+1, "id:", defaultKeyID)
		}

		if !keyIDForm.MatchString(id) {
			return nil, fmt.Errorf("%w: entry %d key id %q must match %s", ErrKeyringInvalid, i+1, id, keyIDForm.String())
		}
		if _, dup := kr.byID[id]; dup {
			// The id is what a blob names; two keys sharing one means the
			// header no longer identifies the sealing key.
			return nil, fmt.Errorf("%w: key id %q appears more than once", ErrKeyringInvalid, id)
		}

		keyBytes, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("%w: decoding key %q: %w", ErrKeyringInvalid, id, err)
		}
		if len(keyBytes) != aesKeyLen {
			return nil, fmt.Errorf("%w: key %q must decode to %d bytes, got %d", ErrKeyringInvalid, id, aesKeyLen, len(keyBytes))
		}
		block, err := aes.NewCipher(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: creating AES cipher for key %q: %w", ErrKeyringInvalid, id, err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("%w: creating GCM for key %q: %w", ErrKeyringInvalid, id, err)
		}

		kr.byID[id] = gcm
		kr.ids = append(kr.ids, id)
	}

	kr.primaryID = kr.ids[0]
	return kr, nil
}

// KeyringFromEnv parses the keyring held in the variable name, read through
// getenv — pass os.Getenv. forge/pkg never reads the ambient environment
// itself (a library changes behaviour only through its arguments), so the
// caller owns where the value comes from. An unset or blank variable yields
// ErrKeyNotConfigured (wrapped with the name); a malformed one yields
// ErrKeyringInvalid. Callers that accept "absent" but treat "malformed" as
// fatal can branch on errors.Is.
func KeyringFromEnv(name string, getenv func(string) string) (*Keyring, error) {
	raw := getenv(name)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("%w: %s is unset", ErrKeyNotConfigured, name)
	}
	kr, err := ParseKeyring(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return kr, nil
}

// PrimaryKeyID returns the id new writes seal under.
func (k *Keyring) PrimaryKeyID() string { return k.primaryID }

// KeyIDs returns every id available for opening, primary first.
func (k *Keyring) KeyIDs() []string { return append([]string(nil), k.ids...) }
