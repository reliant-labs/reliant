// Copyright (c) 2025 Reliant Labs
package gitpublish

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidLeaseSHA(t *testing.T) {
	t.Parallel()
	sha1 := strings.Repeat("ab", 20)
	for v, want := range map[string]bool{
		sha1:                     true,
		strings.Repeat("0f", 32): true, // sha-256
		strings.Repeat("0", 40):  false,
		strings.ToUpper(sha1):    false,
		sha1[:12]:                false,
		"":                       false,
		"true":                   false,
		sha1[:39] + "g":          false,
		"refs/heads/main":        false,
		strings.Repeat("ab", 21): false,
		sha1[:38] + ":x":         false,
		" " + sha1[:39]:          false,
	} {
		assert.Equal(t, want, ValidLeaseSHA(v), "%q", v)
	}
}

func TestHasCapability(t *testing.T) {
	t.Parallel()
	js := func(s string) *string { return &s }
	assert.True(t, HasCapability(js(`["bash","git.publish.force_with_lease"]`), CapabilityForceWithLease))
	assert.False(t, HasCapability(js(`["bash","read_file"]`), CapabilityForceWithLease))
	assert.False(t, HasCapability(js(`["git.publish.force_with_lease_v0"]`), CapabilityForceWithLease), "matched exactly")
	assert.False(t, HasCapability(js(`[]`), CapabilityForceWithLease))
	assert.False(t, HasCapability(js(`not json`), CapabilityForceWithLease))
	assert.False(t, HasCapability(nil, CapabilityForceWithLease))
}
