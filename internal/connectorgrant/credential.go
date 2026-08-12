// Copyright (c) 2025 Reliant Labs

package connectorgrant

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// CredentialPrefix marks a connector credential.
	//
	// It is deliberately distinct from the rlnt_pat_ PAT prefix. The two
	// authenticate very different things — a PAT is a user acting as
	// themselves with full access, a connector credential is a third-party
	// model confined to one grant — and a credential that is visibly the wrong
	// kind is rejected on sight instead of being looked up in the wrong table.
	CredentialPrefix = "rlnt_conn_"

	// credentialRandomBytes yields 40 base62 characters, matching the entropy
	// of the existing PAT format.
	credentialRandomBytes = 30

	// displayPrefixLen is how much of the credential is kept in cleartext for
	// the UI to identify a connector without holding the secret.
	displayPrefixLen = 8
)

const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// GenerateCredential mints a connector credential. The raw value is returned
// once, for display at creation; only the hash is ever persisted.
func GenerateCredential() (raw, hash, displayPrefix string, err error) {
	buf := make([]byte, credentialRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generate connector credential: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(CredentialPrefix)
	for _, b := range buf {
		sb.WriteByte(base62Chars[int(b)%len(base62Chars)])
	}
	raw = sb.String()

	return raw, HashCredential(raw), raw[:len(CredentialPrefix)+displayPrefixLen], nil
}

// HashCredential returns the SHA-256 hex digest used for lookup.
func HashCredential(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// IsCredentialFormat reports whether a bearer token looks like a connector
// credential. Checked before any database work so a malformed or wrong-kind
// token costs nothing.
func IsCredentialFormat(token string) bool {
	return strings.HasPrefix(token, CredentialPrefix) && len(token) > len(CredentialPrefix)+10
}
