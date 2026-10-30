// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"url userinfo", "dial postgres://app:hunter2@db:5432/x", "dial postgres://[redacted]@db:5432/x"},
		{"key=value", "PASSWORD=hunter2 next", "PASSWORD=[redacted] next"},
		{"json key", `{"api_key": "abc123"}`, `{"api_key": "[redacted]"}`},
		{"authorization header", "Authorization: Bearer abc.def", "Authorization: [redacted]"},
		{"bare bearer", "sent bearer abcdef123", "sent bearer [redacted]"},
		{"jwt", "tok eyJhbGciOi.eyJzdWIiOi.c2lnbmF0dXJl end", "tok [redacted] end"},
		{"anthropic key", "key sk-ant-api03-abcdefghij", "key [redacted]"},
		{"github token", "ghp_abcdefghijklmnop", "[redacted]"},
		{"aws key", "AKIAABCDEFGHIJKLMNOP", "[redacted]"},
		{"long opaque run", strings.Repeat("a", 40), "[redacted]"},
		{"env password suffix", "DATABASE_PASSWORD=hunter2hunter2", "DATABASE_PASSWORD=[redacted]"},
		{"env pgpassword", "PGPASSWORD=hunter2", "PGPASSWORD=[redacted]"},
		{"env stripe secret key", "STRIPE_SECRET_KEY=whsec_abc123def", "STRIPE_SECRET_KEY=[redacted]"},
		{"env api token", "MY_API_TOKEN=abc123", "MY_API_TOKEN=[redacted]"},
		{"env export quoted", `export GITHUB_TOKEN="abc/def"`, `export GITHUB_TOKEN="[redacted]"`},
		{"colon form", "client_secret: s3cr3t/value+x==", "client_secret: [redacted]"},
		{"aws secret with slash", "aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "aws_secret_access_key = [redacted]"},
		{"value with slash plus equals", "password=ab/cd+ef==", "password=[redacted]"},
		{"x-api-key header", "x-api-key: abc123", "x-api-key: [redacted]"},
		{"flag space", "psql --password hunter2 -h db", "psql --password [redacted] -h db"},
		{"flag equals", "run --api-key=abc123 --verbose", "run --api-key=[redacted] --verbose"},
		{"flag prefixed", "cli --db-password hunter2", "cli --db-password [redacted]"},
		{"flag secret-key quoted", `cli --secret-key "abc"`, `cli --secret-key "[redacted]"`},
		{"flag followed by flag kept", "cli --password --verbose", "cli --password --verbose"},
		{"flag token-file kept", "cli --token-file /etc/tok", "cli --token-file /etc/tok"},
		{"pem block", "key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA/1+x\nab/cd+ef==\n-----END RSA PRIVATE KEY-----\nafter", "key:\n[redacted]\nafter"},
		{"pem pkcs8", "-----BEGIN PRIVATE KEY-----\nabc/def+ghi\n-----END PRIVATE KEY-----", "[redacted]"},
		{"pem json escaped", `{"private_key": "-----BEGIN PRIVATE KEY-----\nabc/def\n-----END PRIVATE KEY-----\n"}`, `{"private_key": "[redacted]"}`},
		{"pem unterminated", "-----BEGIN EC PRIVATE KEY-----\nabc/def+ghi", "[redacted]"},
		{"public key kept", "-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----", "-----BEGIN PUBLIC KEY-----\nabc\n-----END PUBLIC KEY-----"},
		{"tokenizer file kept", "see internal/lex/tokenizer.go:12", "see internal/lex/tokenizer.go:12"},
		{"field name without value kept", "the password_reset_url field is empty", "the password_reset_url field is empty"},
		{"word password kept", "reset your password to continue", "reset your password to continue"},
		{"flag then prose is redacted, not guessed", "pass --password to set it", "pass --password [redacted] set it"},
		{"uuid kept", "run 123e4567-e89b-12d3-a456-426614174000", "run 123e4567-e89b-12d3-a456-426614174000"},
		{"path kept", "internal/cli/report_bug.go:12", "internal/cli/report_bug.go:12"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactSecrets(tc.in); got != tc.want {
				t.Errorf("redactSecrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
