// Copyright (c) 2025 Reliant Labs

package connections

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The token response an oauth2 sender_id is evaluated over has every token
// removed, at any depth: the expression only needs to name a person.
func TestWithoutTokensDropsEveryTokenField(t *testing.T) {
	raw := map[string]any{
		"ok": true, "access_token": "xoxb-bot", "refresh_token": "xoxe-1", "token_type": "bot", "id_token": "eyJ",
		"team": map[string]any{"id": "T0ACME"},
		"authed_user": map[string]any{
			"id": "U0HUMAN", "scope": "search:read", "access_token": "xoxp-user", "refresh_token": "xoxe-user",
		},
	}
	assert.Equal(t, map[string]any{
		"ok":          true,
		"team":        map[string]any{"id": "T0ACME"},
		"authed_user": map[string]any{"id": "U0HUMAN", "scope": "search:read"},
	}, withoutTokens(raw))
	assert.Equal(t, "xoxp-user", raw["authed_user"].(map[string]any)["access_token"], "the original is not modified")
}
