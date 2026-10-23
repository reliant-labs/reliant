package orm

import (
	"encoding/base64"
	"fmt"
)

// EncodeCursor encodes a primary key value into an opaque page cursor token.
// The cursor is base64url-encoded (no padding) for safe use in query strings.
func EncodeCursor(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

// DecodeCursor decodes a page cursor token back into the primary key value.
// Returns an error if the token is empty or not valid base64url.
func DecodeCursor(token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("empty cursor token")
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("invalid cursor token: %w", err)
	}
	return string(b), nil
}
