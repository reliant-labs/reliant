// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestInferOAuthCallbackConfig(t *testing.T) {
	tests := []struct {
		name                 string
		authorizeURLTemplate string
		want                 oauthCallbackConfig
	}{
		{
			name:                 "claude uses localhost random callback",
			authorizeURLTemplate: "https://claude.ai/oauth/authorize?redirect_uri={redirect_uri}",
			want: oauthCallbackConfig{
				listenHost:   "127.0.0.1",
				redirectHost: "localhost",
				callbackPath: "/callback",
				fixedPort:    0,
			},
		},
		{
			name:                 "codex advertises localhost fixed callback",
			authorizeURLTemplate: "https://auth.openai.com/oauth/authorize?redirect_uri={redirect_uri}",
			want: oauthCallbackConfig{
				listenHost:   "127.0.0.1",
				redirectHost: "localhost",
				callbackPath: "/auth/callback",
				fixedPort:    1455,
			},
		},
		{
			name:                 "invalid url falls back to defaults",
			authorizeURLTemplate: "://bad-url",
			want: oauthCallbackConfig{
				listenHost:   "127.0.0.1",
				redirectHost: "localhost",
				callbackPath: "/auth/callback",
				fixedPort:    0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, inferOAuthCallbackConfig(tt.authorizeURLTemplate))
		})
	}
}

func TestUTF8Sanitization(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool // whether input is already valid UTF-8
	}{
		{
			name:  "valid ASCII",
			input: "hello world",
			valid: true,
		},
		{
			name:  "valid UTF-8 with unicode",
			input: "hello 🌍 world café",
			valid: true,
		},
		{
			name:  "invalid UTF-8 bytes",
			input: "hello \x80\x81\x82 world",
			valid: false,
		},
		{
			name:  "mixed valid and invalid",
			input: "grep output: \xff\xfe some text \x80",
			valid: false,
		},
		{
			name:  "empty string",
			input: "",
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sanitized := strings.ToValidUTF8(tt.input, "\uFFFD")
			assert.True(t, utf8.ValidString(sanitized), "sanitized string must be valid UTF-8")

			if tt.valid {
				assert.Equal(t, tt.input, sanitized, "valid input should be unchanged")
			} else {
				assert.NotEqual(t, tt.input, sanitized, "invalid input should be modified")
				assert.Contains(t, sanitized, "\uFFFD", "invalid bytes should be replaced with U+FFFD")
			}
		})
	}
}
