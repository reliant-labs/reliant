// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

// A webhook trigger's token is a bearer secret the server generates. 32
// random bytes (256 bits) is past any guessing attack, so the stored form is
// a plain SHA-256: there is nothing for a slow hash to protect, and a fast one
// keeps verification constant-time and cheap on a public endpoint.

// webhookTokenBytes is the token's entropy.
const webhookTokenBytes = 32

// NewWebhookToken returns a fresh token and the hash to store for it.
func NewWebhookToken() (token string, hash []byte, err error) {
	raw := make([]byte, webhookTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate webhook token: %w", err)
	}
	// "whk_" makes a leaked token recognisable to secret scanners and to a
	// human reading a log.
	token = "whk_" + base64.RawURLEncoding.EncodeToString(raw)
	return token, HashWebhookToken(token), nil
}

// HashWebhookToken is the stored form of a token.
func HashWebhookToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// WebhookTokenMatches reports, in constant time, whether token hashes to
// stored. An empty stored hash never matches.
func WebhookTokenMatches(stored []byte, token string) bool {
	if len(stored) != sha256.Size || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare(stored, HashWebhookToken(token)) == 1
}

// WebhookSecretAAD binds a sealed HMAC secret to its trigger: a ciphertext
// copied onto another trigger's row does not open. The vault adds the owner's
// tenant.
func WebhookSecretAAD(triggerID string) []byte {
	return []byte("trigger_webhook_secret\x00" + triggerID)
}
