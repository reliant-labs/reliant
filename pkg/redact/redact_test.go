package redact

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// AWS's documented example credentials, split so a secret scanner does not
// read the test as a leaked key.
const (
	awsKeyID      = "AKIA" + "IOSFODNN7EXAMPLE"
	awsSecret     = "wJalrXUtnFEMI/K7MDENG" + "/bPxRfiCYEXAMPLEKEY"
	awsSecretPlus = "9drTJvcXLB89EXAMPLE" + "+LYFb7Hy3VhN/Ls3jR0mU"
)

func TestSecrets(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"url userinfo", "dial postgres://app:hunter2@db:5432/x", "dial postgres://[redacted]@db:5432/x"},
		{"key=value", "PASSWORD=hunter2 next", "PASSWORD=[redacted] next"},
		{"json key", `{"api_key": "abc123"}`, `{"api_key": "[redacted]"}`},
		{"authorization header", "Authorization: Bearer abc.def", "Authorization: [redacted]"},
		{"bare bearer", "sent bearer abcdef123", "sent bearer [redacted]"},
		{"jwt", "tok eyJhbGciOi.eyJzdWIiOi.c2lnbmF0dXJl end", "tok [redacted] end"},
		{"anthropic key", "key sk-ant-api03-abcdefghij", "key [redacted]"},
		{"github token", "ghp_abcdefghijklmnop", "[redacted]"},
		{"aws key", "AKIA" + "ABCDEFGHIJKLMNOP", "[redacted]"},
		{"long opaque run", strings.Repeat("a", 41), "[redacted]"},
		{"env password suffix", "DATABASE_PASSWORD=hunter2hunter2", "DATABASE_PASSWORD=[redacted]"},
		{"env pgpassword", "PGPASSWORD=hunter2", "PGPASSWORD=[redacted]"},
		{"env stripe secret key", "STRIPE_SECRET_KEY=whsec_abc123def", "STRIPE_SECRET_KEY=[redacted]"},
		{"env api token", "MY_API_TOKEN=abc123", "MY_API_TOKEN=[redacted]"},
		{"env export quoted", `export GITHUB_TOKEN="abc/def"`, `export GITHUB_TOKEN="[redacted]"`},
		{"colon form", "client_secret: s3cr3t/value+x==", "client_secret: [redacted]"},
		{"aws secret with slash", "aws_secret_access_key = " + awsSecret, "aws_secret_access_key = [redacted]"},
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

		// AWS secret access keys: the bare key holds '/', which the long-run
		// rule excludes.
		{"aws secret bare", awsSecret, "[redacted]"},
		{"aws secret bare in output", "$ aws configure get aws_secret_access_key\n" + awsSecret + "\n$", "$ aws configure get aws_secret_access_key\n[redacted]\n$"},
		{"aws secret bare with plus", "key: " + awsSecretPlus + " end", "key: [redacted] end"},
		{"aws secret after key name, whitespace", "aws_secret_access_key   " + awsSecret + " shared-credentials-file", "aws_secret_access_key   [redacted] shared-credentials-file"},
		{"aws secret key name all lowercase value", "secret_key abcdefghijklmnopqrstuvwxyzabcdefghij/klm", "secret_key [redacted]"},
		{"aws secret env", "AWS_SECRET_ACCESS_KEY=" + awsSecret, "AWS_SECRET_ACCESS_KEY=[redacted]"},
		{"aws secret json", `{"SecretAccessKey": "` + awsSecret + `"}`, `{"SecretAccessKey": "[redacted]"}`},
		{"aws secret credentials file", "[default]\naws_access_key_id = " + awsKeyID + "\naws_secret_access_key = " + awsSecret, "[default]\naws_access_key_id = [redacted]\naws_secret_access_key = [redacted]"},
		{"aws masked secret kept", "secret_key     ****************EFGH shared-credentials-file", "secret_key     ****************EFGH shared-credentials-file"},
		{"git sha kept", "fixed in 6d6ffcf96c1e0b7d2f5a8e3c4b9d0a1f2e3c4d5e on main", "fixed in 6d6ffcf96c1e0b7d2f5a8e3c4b9d0a1f2e3c4d5e on main"},
		{"git sha alone kept", "6d6ffcf96c1e0b7d2f5a8e3c4b9d0a1f2e3c4d5e", "6d6ffcf96c1e0b7d2f5a8e3c4b9d0a1f2e3c4d5e"},
		{"git sha in url kept", "https://github.com/reliant-labs/forge/commit/6d6ffcf96c1e0b7d2f5a8e3c4b9d0a1f2e3c4d5e", "https://github.com/reliant-labs/forge/commit/6d6ffcf96c1e0b7d2f5a8e3c4b9d0a1f2e3c4d5e"},
		{"sha256 digest redacted", "sha256:" + strings.Repeat("ab", 32), "sha256:[redacted]"},
		{"upper-case hex 40 redacted", strings.Repeat("AB", 20), "[redacted]"},
		{"40-char lowercase path kept", "see frontends/web/src/components/sessions/x", "see frontends/web/src/components/sessions/x"},
		{"40-char lowercase word run kept", "abcdefghijklmnopqrstuvwxyz/abcdefghijklm", "abcdefghijklmnopqrstuvwxyz/abcdefghijklm"},
		{"secret key prose kept", "the secret key is rotated nightly", "the secret key is rotated nightly"},

		// Cookies.
		{"cookie header", "Cookie: session=abcdef0123456789abcdef", "Cookie: [redacted]"},
		{"cookie header every pair", "> Cookie: session=abc123; csrf=def456; theme=dark\n> Accept: */*", "> Cookie: [redacted]\n> Accept: */*"},
		{"set-cookie header", "< Set-Cookie: sid=s%3Aabc.def; Path=/; HttpOnly; Secure\n< Vary: Origin", "< Set-Cookie: [redacted]\n< Vary: Origin"},
		{"cookie header lower case", "cookie: a=b", "cookie: [redacted]"},
		{"curl -H cookie", `curl -H 'Cookie: session=abc; csrf=def' https://x.test/`, `curl -H 'Cookie: [redacted]' https://x.test/`},
		{"curl --cookie", `curl --cookie "session=abc; csrf=def" https://x.test/`, `curl --cookie [redacted] https://x.test/`},
		{"curl --cookie=", "curl --cookie=session=abc https://x.test/", "curl --cookie=[redacted] https://x.test/"},
		{"curl --cookie-jar kept", "curl --cookie-jar /tmp/jar https://x.test/", "curl --cookie-jar /tmp/jar https://x.test/"},
		{"json cookie", `{"cookie": "session=abc; csrf=def"}`, `{"cookie": "[redacted]"}`},
		{"json set-cookie list", `{"set-cookie": ["sid=abc; Path=/"]}`, `{"set-cookie": ["[redacted]"]}`},
		{"json list value", `{"tokens": ["abc123"]}`, `{"tokens": ["[redacted]"]}`},
		{"env cookie", "SESSION_COOKIE=abc123 next", "SESSION_COOKIE=[redacted] next"},
		{"cookie prose kept", "clear the cookies and retry", "clear the cookies and retry"},
		{"cookie-jar flag name kept", "the cookie jar is empty", "the cookie jar is empty"},

		// Reliant claim tokens and claim links.
		{"claim token", "token rcl_q3Xb7Yk9Zz1aB2cD3eF4gH5iJ6kL7mN8oP9qR0sT1u end", "token [redacted] end"},
		{"claim token with dash", "token rcl_q3Xb7Yk9-Zz1aB2cD3eF4gH5iJ6kL7mN8oP9qR0sT1u end", "token [redacted] end"},
		{"claim token with dash and underscore", "rcl_-_q3Xb7Yk9-Zz1_B2cD3eF4gH5iJ6kL7mN8oP9qR0sT", "[redacted]"},
		{"claim link", "claim it: https://app.reliant.dev/claim#rcl_q3Xb7Yk9-Zz1aB2cD3eF4gH5iJ6kL7mN8oP9qR0sT1u (72h)", "claim it: https://app.reliant.dev/claim#[redacted] (72h)"},
		{"claim link any fragment", "https://app.reliant.dev/claim#abc-def", "https://app.reliant.dev/claim#[redacted]"},
		{"claim link markdown", "[claim](https://app.reliant.dev/claim/#rcl_a-b_c)", "[claim](https://app.reliant.dev/claim/#[redacted])"},
		{"claim link quoted", `{"claim_url": "https://app.reliant.dev/claim#rcl_a-b"}`, `{"claim_url": "https://app.reliant.dev/claim#[redacted]"}`},
		{"doc anchor kept", "see https://docs.reliant.dev/deploy#claimable-deploys", "see https://docs.reliant.dev/deploy#claimable-deploys"},
		{"claim path without fragment kept", "open https://app.reliant.dev/claim to sign in", "open https://app.reliant.dev/claim to sign in"},
		{"rcl word kept", "the rcl prefix marks a claim token", "the rcl prefix marks a claim token"},

		// Ordinary text is left alone.
		{"prose kept", "The deploy failed: exit status 7 (billing). Retry after setup.", "The deploy failed: exit status 7 (billing). Retry after setup."},
		{"uuid list kept", "ids 123e4567-e89b-12d3-a456-426614174000, 00000000-0000-0000-0000-000000000000", "ids 123e4567-e89b-12d3-a456-426614174000, 00000000-0000-0000-0000-000000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Secrets(tc.in)
			if got != tc.want {
				t.Errorf("Secrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Text is redacted twice on its way out (the client, then the
			// ingest): a second pass must not re-match a Marker.
			if again := Secrets(got); again != got {
				t.Errorf("Secrets is not idempotent: Secrets(%q) = %q", got, again)
			}
		})
	}
}

func TestHomeDirs(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"open /home/alice/proj/forge.yaml: denied", "open ~/proj/forge.yaml: denied"},
		{"/Users/bob/src", "~/src"},
		{`C:\Users\carol\src\x.go`, `~\src\x.go`},
		{"/srv/app/main.go", "/srv/app/main.go"},
	} {
		if got := HomeDirs(tc.in); got != tc.want {
			t.Errorf("HomeDirs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBounded(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"exact", "héllo", 6, "héllo"},
		{"exact multibyte", strings.Repeat("😀", 4), 16, strings.Repeat("😀", 4)},
		{"one over boundary", strings.Repeat("😀", 4) + "!", 16, "😀" + TruncatedMarker},
		{"redaction expansion", "token=x", 15, "tok" + TruncatedMarker},
		{"marker included", strings.Repeat("x ", 20), 16, "x x " + TruncatedMarker},
		{"unicode boundary", strings.Repeat("界", 10), 16, "界" + TruncatedMarker},
		{"emoji boundary", strings.Repeat("😀", 10), 16, "😀" + TruncatedMarker},
		{"small bound", "héllo wörld", 5, " [tru"},
		{"marker only", strings.Repeat("界", 10), len(TruncatedMarker), TruncatedMarker},
		{"one byte", "界界", 1, " "},
		{"invalid input", "bad \xff byte", 10, " [truncate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Bounded(tc.in, tc.max)
			if got != tc.want || len(got) > tc.max || !utf8.ValidString(got) {
				t.Errorf("Bounded = %q (%d bytes), want %q within %d valid UTF-8 bytes", got, len(got), tc.want, tc.max)
			}
		})
	}
	if got := Bounded("short", 10); got != "short" {
		t.Errorf("Bounded under the bound = %q", got)
	}
	if got := Bounded("token=abc /home/al/x", 0); got != "token=[redacted] ~/x" {
		t.Errorf("Bounded unbounded = %q", got)
	}
	if got := Bounded("bad \xff byte", 0); got != "bad \uFFFD byte" {
		t.Errorf("Bounded invalid utf-8 = %q", got)
	}
}
