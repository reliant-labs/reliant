package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// PATPrefix is the prefix for all Reliant PAT tokens.
	PATPrefix = "rlnt_pat_"
	// patRandomBytes is the number of random bytes in a PAT (30 bytes → 40 base62 chars).
	patRandomBytes = 30
	// PATTokenPrefixLen is how many chars of the raw token to store for UI display.
	PATTokenPrefixLen = 8
)

// base62 charset for token encoding
const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// GeneratePAT creates a new PAT token. Returns the raw token (show once to user)
// and the token hash (store in DB). Format: rlnt_pat_<base62 random>.
func GeneratePAT() (rawToken string, tokenHash string, tokenPrefix string, err error) {
	buf := make([]byte, patRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("failed to generate PAT: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(PATPrefix)
	for _, b := range buf {
		sb.WriteByte(base62Chars[int(b)%len(base62Chars)])
	}
	raw := sb.String()
	hash := HashPAT(raw)
	prefix := raw[:len(PATPrefix)+PATTokenPrefixLen] // e.g. "rlnt_pat_AbCdEfGh"

	return raw, hash, prefix, nil
}

// HashPAT returns the SHA-256 hex digest of a raw PAT token.
func HashPAT(rawToken string) string {
	h := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(h[:])
}

// IsPATFormat checks if a string looks like a Reliant PAT.
func IsPATFormat(token string) bool {
	return strings.HasPrefix(token, PATPrefix) && len(token) > len(PATPrefix)+10
}

// PATValidator validates a raw PAT token and returns the associated user ID.
type PATValidator interface {
	// ValidatePAT checks a raw token against the DB. Returns the user ID and
	// optionally the PAT-bound daemon ID (empty if unbound). Returns an error
	// if the token is invalid, revoked, or expired.
	ValidatePAT(ctx context.Context, rawToken string) (userID string, patID string, daemonID string, err error)
}
