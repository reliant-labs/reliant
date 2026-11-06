package crypto

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// envelopeMagic marks a versioned blob and doubles as the format version.
//
// Four bytes of magic make the pre-keyring format (raw nonce ‖ ciphertext)
// DETECTABLE rather than merely unparseable: a random 12-byte nonce will not
// begin with these bytes, so an old row is identified as old instead of being
// mis-parsed into a nonsense key id. It also gives the next format change a
// place to stand — a "CPK2" can coexist with CPK1 in one column.
//
// The bytes stay "CPK1" (not renamed for forge) because they are the on-disk
// format control-plane already wrote.
var envelopeMagic = []byte("CPK1")

// Envelope layout, all offsets from byte 0:
//
//	[0:4]        magic       "CPK1"
//	[4]          idLen       length of the key id, 1..maxKeyIDLen
//	[5:5+idLen]  keyID       the id of the key that sealed this blob
//	then         nonce       GCM nonce, 12 bytes
//	then         ciphertext  GCM output (includes the auth tag)
//
// WHY THE KEY ID IS IN THE BYTES AND NOT IN A COLUMN. A ciphertext MOVES: it
// is restored from backups, joined across tables, pasted into tickets, or
// transplanted between rows by an attacker who can write storage. In each, a
// blob and a sibling key_id column can be separated, and a blob separated
// from its key id is unopenable with no way to learn which key it needed.
// Self-describing bytes are sufficient on their own, like the nonce. A column
// would also be a second source of truth that can DISAGREE with the blob.
//
// The column's one real benefit, queryability ("which rows still use the old
// key?"), is kept: the header sits at a fixed offset, so it is a prefix match,
//
//	WHERE substring(ciphertext FROM 1 FOR 7) = '\x43504b31027631'::bytea
//	      -- "CPK1" ‖ 0x02 ‖ "v1"
//
// and EnvelopePrefixForKeyID builds that prefix so nobody hand-assembles it.
const (
	magicLen       = 4
	idLenOffset    = magicLen
	keyIDOffset    = magicLen + 1
	minEnvelopeLen = keyIDOffset + 1 // magic + idLen + at least one id byte
)

// EnvelopePrefixForKeyID returns the exact leading bytes of every blob sealed
// under keyID — the SQL prefix for "which rows still use this key".
func EnvelopePrefixForKeyID(keyID string) ([]byte, error) {
	if !keyIDForm.MatchString(keyID) {
		return nil, fmt.Errorf("%w: %q must match %s", ErrKeyringInvalid, keyID, keyIDForm.String())
	}
	out := make([]byte, 0, keyIDOffset+len(keyID))
	out = append(out, envelopeMagic...)
	out = append(out, byte(len(keyID)))
	out = append(out, keyID...)
	return out, nil
}

// Seal encrypts plaintext under the PRIMARY key and cryptographically binds it
// to aad and to its own envelope header.
//
// aad is not encrypted and not stored: the opener re-derives it from the
// context it believes the blob belongs to, and Open with different aad fails
// authentication. That makes a sealed value NON-TRANSPLANTABLE, a stronger
// claim than any access check — a WHERE clause does nothing if the ciphertext
// is copied into a row the wrong caller legitimately owns. nil aad is the
// unbound case. A blob sealed with aad does not open with nil aad.
//
// New writes always use the primary, with no per-call override: a caller that
// could pick its key could keep writing under a key a rotation is retiring,
// and the backfill would never finish.
func (k *Keyring) Seal(plaintext, aad []byte) ([]byte, error) {
	gcm := k.byID[k.primaryID]
	header, err := EnvelopePrefixForKeyID(k.primaryID)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if err := k.readNonce(nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	ct := gcm.Seal(nil, nonce, plaintext, bindHeader(header, aad))

	out := make([]byte, 0, len(header)+len(nonce)+len(ct))
	out = append(out, header...)
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// Open decrypts a blob, selecting the key its envelope names. Failure modes
// are distinct sentinels:
//
//	ErrUnversionedCiphertext — sealed before the envelope existed
//	ErrUnknownKeyID          — a key that was removed from the keyring
//	ErrKeyringInvalid        — corrupt envelope header
//	(wrapped GCM failure)    — tampering or wrong aad; the only security event
//
// Collapsing a rotation mistake into an authentication failure sends the
// operator on an intrusion hunt when the answer is "the key you removed is
// still in use".
func (k *Keyring) Open(ciphertext, aad []byte) ([]byte, error) {
	keyID, header, body, err := decodeEnvelope(ciphertext)
	if err != nil {
		return nil, err
	}

	gcm, ok := k.byID[keyID]
	if !ok {
		// Name the key and list what we hold so the operator knows exactly
		// which entry to restore.
		return nil, fmt.Errorf("%w: key id %q (keyring holds: %s)",
			ErrUnknownKeyID, keyID, strings.Join(k.ids, ", "))
	}

	ns := gcm.NonceSize()
	if len(body) < ns {
		return nil, errors.New("ciphertext too short")
	}
	pt, err := gcm.Open(nil, body[:ns], body[ns:], bindHeader(header, aad))
	if err != nil {
		return nil, fmt.Errorf("decrypting under key id %q: %w", keyID, err)
	}
	return pt, nil
}

// bindHeader is the AAD actually passed to GCM: envelope header ‖ caller aad.
//
// Binding the key id means an attacker cannot edit it in place. The id SELECTS
// THE KEY, so an unbound id is an attacker-chosen input to key selection.
// Editing the id of a bound blob yields an authentication failure. The cost is
// that a blob cannot be re-wrapped to a new key without re-sealing, which is
// no loss here since there is no data-key layer; revisit if one is added.
//
// The header is a prefix and self-delimiting (fixed magic, length-prefixed
// id), so header ‖ aad is injective.
func bindHeader(header, aad []byte) []byte {
	out := make([]byte, 0, len(header)+len(aad))
	out = append(out, header...)
	out = append(out, aad...)
	return out
}

// decodeEnvelope splits a blob into key id, header (for AAD) and the
// nonce ‖ ciphertext body.
//
// A BLOB WITH NO ENVELOPE IS REFUSED, NOT TREATED AS AN IMPLICIT "v1". Implicit
// v1 is a permanent second format with no expiry (nothing proves the last
// unprefixed row is gone), and, decisively, a decrypt oracle: v1 would have to
// open with no header in the AAD, so an attacker who can write storage strips
// the header from a v1-sealed blob and has it opened under an AAD the sealer
// never authorized. Header binding is only load-bearing if the unbound
// spelling of the same ciphertext is not also accepted. Legacy rows should be
// re-minted, not re-read.
func decodeEnvelope(blob []byte) (keyID string, header, body []byte, err error) {
	if len(blob) < minEnvelopeLen || !bytes.Equal(blob[:magicLen], envelopeMagic) {
		return "", nil, nil, fmt.Errorf("%w: no %q header (%d bytes); it was sealed by the pre-keyring format and must be re-minted, not re-read",
			ErrUnversionedCiphertext, envelopeMagic, len(blob))
	}

	idLen := int(blob[idLenOffset])
	if idLen == 0 || idLen > maxKeyIDLen {
		return "", nil, nil, fmt.Errorf("%w: envelope declares a %d-byte key id (limit %d)", ErrKeyringInvalid, idLen, maxKeyIDLen)
	}
	if len(blob) < keyIDOffset+idLen {
		return "", nil, nil, fmt.Errorf("%w: envelope declares a %d-byte key id but only %d bytes follow the header",
			ErrKeyringInvalid, idLen, len(blob)-keyIDOffset)
	}

	keyID = string(blob[keyIDOffset : keyIDOffset+idLen])
	if !keyIDForm.MatchString(keyID) {
		return "", nil, nil, fmt.Errorf("%w: envelope key id %q must match %s", ErrKeyringInvalid, keyID, keyIDForm.String())
	}
	return keyID, blob[:keyIDOffset+idLen], blob[keyIDOffset+idLen:], nil
}
