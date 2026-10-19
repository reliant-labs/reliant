// Copyright (c) 2025 Reliant Labs

package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProviderRefRoundTrips(t *testing.T) {
	cases := []struct {
		in   string
		want ProviderRef
	}{
		{"claude", ProviderRef{Driver: "claude"}},
		{"codex", ProviderRef{Driver: "codex"}},
		{"local:daemon-123", ProviderRef{Driver: "local", Instance: "daemon-123"}},
		{"claude:6f1c2d3e-0000-4000-8000-000000000001", ProviderRef{Driver: "claude", Instance: "6f1c2d3e-0000-4000-8000-000000000001"}},
		{"claude:a@b.c", ProviderRef{Driver: "claude", Instance: "a@b.c"}},
		{"claude:work:team@acme.com", ProviderRef{Driver: "claude", Instance: "work:team@acme.com"}},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseProviderRef(c.in)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
			assert.Equal(t, c.in, got.String())
		})
	}
}

func TestParseProviderRefRejects(t *testing.T) {
	for _, in := range []string{"", ":", ":abc", "gpt", "nosuch:abc", "claude:", "Claude"} {
		t.Run(in, func(t *testing.T) {
			_, err := ParseProviderRef(in)
			assert.Error(t, err)
		})
	}
}

func TestParseProviderRefTrimsSpace(t *testing.T) {
	got, err := ParseProviderRef("  codex:x  ")
	require.NoError(t, err)
	assert.Equal(t, ProviderRef{Driver: "codex", Instance: "x"}, got)
}
