// Copyright (c) 2025 Reliant Labs
package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every shape in the audit's list must come out masked, wherever it sits in a
// value. The fixtures are assembled from pieces so no real-looking credential
// is committed to the tree.
func TestSanitizeMasksSecretShapes(t *testing.T) {
	jwt := "eyJ" + "hbGciOiJIUzI1NiJ9" + "." + "eyJ" + "zdWIiOiIxMjM0NTY3ODkwIn0" + "." + "dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	cases := []struct {
		name    string
		in      string
		secret  string
		wantKey string // text that must survive, so the line still says what it was
	}{
		{"bearer", "calling with Authorization: Bearer abcdef0123456789abcdef", "abcdef0123456789abcdef", "Bearer"},
		{"bearer lowercase", "header bearer abcdef0123456789abcdef", "abcdef0123456789abcdef", ""},
		{"basic", "Authorization: Basic dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA==", "Basic"},
		{"reliant access token", "pat rlat_" + "abcdefghijklmnop1234", "abcdefghijklmnop1234", ""},
		{"sk key", "key " + "sk-" + "ant-api03-abcdefghijklmnopqrstuv", "abcdefghijklmnopqrstuv", ""},
		{"github pat", "tok " + "ghp_" + "abcdefghijklmnopqrstuvwxyz0123", "abcdefghijklmnopqrstuvwxyz0123", ""},
		{"github fine-grained", "tok " + "github_pat_" + "11ABCDEFG0abcdefghij_klmnopqrstuvwx", "klmnopqrstuvwx", ""},
		{"gho", "tok " + "gho_" + "abcdefghijklmnopqrstuvwxyz0123", "abcdefghijklmnopqrstuvwxyz0123", ""},
		{"slack", "tok " + "xoxb-" + "1234567890-abcdefghij", "1234567890-abcdefghij", ""},
		{"aws key id", "id " + "AKIA" + "ABCDEFGHIJKLMNOP", "ABCDEFGHIJKLMNOP", ""},
		{"jwt", "session " + jwt, "dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk", "session"},
		{"password pair", "connect password=hunter2hunter2 host=db", "hunter2hunter2", "host=db"},
		{"quoted password pair", `connect password="correct horse battery" host=db`, "correct horse battery", "host=db"},
		{"token pair", "retry token=abc123def456", "abc123def456", ""},
		{"api key pair", "api_key=abc123def456 next", "abc123def456", "next"},
		{"url userinfo", "cloning https://oauth2:supersecretvalue@github.com/acme/repo.git", "supersecretvalue", "github.com/acme/repo.git"},
		{"url token only", "cloning https://ghs_abcdefghijklmnopqrstuvwxyz0123@github.com/acme/repo.git", "abcdefghijklmnopqrstuvwxyz0123", "github.com/acme/repo.git"},
		{"url query secret", "GET https://api.example.com/v1/x?access_token=abc123def456&page=2", "abc123def456", "page=2"},
		{"json field", `body {"password":"hunter2hunter2","user":"bob"}`, "hunter2hunter2", `"user":"bob"`},
		{"json authorization", `{"authorization": "Token abcdef0123456789"}`, "abcdef0123456789", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitize(tc.in, 4096)
			assert.NotContains(t, got, tc.secret, "secret survived: %s", got)
			assert.Contains(t, got, redacted)
			if tc.wantKey != "" {
				assert.Contains(t, got, tc.wantKey, "masking ate the context")
			}
		})
	}
}

// Ordinary text must pass through byte for byte: a redactor that rewrites
// innocent lines destroys the evidence it exists to protect.
func TestSanitizeLeavesOrdinaryTextAlone(t *testing.T) {
	for _, in := range []string{
		"Session ended; reconnecting",
		"git checkout feature/sk-mask-fix completed in 120ms",
		"token_count=1523 max_tokens=4096",
		"token: refused by gateway",
		"https://github.com/acme/repo.git",
		"/home/workspace/projects/acme/src/main.go:42",
		"skipping task-list for ask-me",
		"émoji ✓ and unicode — stay intact",
	} {
		assert.Equal(t, in, sanitize(in, 4096), in)
	}
}

func TestSanitizeCapsSize(t *testing.T) {
	long := strings.Repeat("a", 5000)
	got := sanitize(long, maxAttrBytes)
	assert.LessOrEqual(t, len(got), maxAttrBytes+len("…[truncated 4488]"))
	assert.Contains(t, got, "…[truncated 4488]", "marker reports exactly how many bytes were dropped")

	exact := strings.Repeat("b", maxAttrBytes)
	assert.Equal(t, exact, sanitize(exact, maxAttrBytes), "at the limit is not over it")
}

func TestSanitizeCountsBytesBeyondTheScanWindow(t *testing.T) {
	huge := strings.Repeat("x", 100_000)
	got := sanitize(huge, maxAttrBytes)
	assert.Contains(t, got, "…[truncated 99488]")
}

// A cut must not split a multi-byte rune, which would emit invalid UTF-8 that
// a JSON encoder then rewrites as U+FFFD mid-word.
func TestSanitizeCutsOnRuneBoundary(t *testing.T) {
	in := strings.Repeat("é", 600) // 1200 bytes
	got := sanitize(in, maxAttrBytes)
	body := got[:strings.Index(got, "…[truncated")]
	assert.True(t, strings.ToValidUTF8(body, "?") == body, "cut left invalid UTF-8")
	assert.NotContains(t, got, "\uFFFD")
}

// Masking runs before the cut, so a secret straddling the limit cannot leave
// its head behind as an unrecognisable fragment.
func TestSanitizeMasksBeforeTruncating(t *testing.T) {
	in := strings.Repeat("a", maxAttrBytes-10) + " rlat_" + "abcdefghijklmnopqrstuvwxyz"
	got := sanitize(in, maxAttrBytes)
	assert.NotContains(t, got, "rlat_")
	assert.NotContains(t, got, "abcdefghij")
}

// redactedLine logs through the real handler chain and returns the decoded
// JSON record, so the test covers ReplaceAttr as slog actually calls it.
func redactedLine(t *testing.T, log func(*slog.Logger)) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: newRedactor().replaceAttr})
	log(slog.New(h))
	var rec map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec), buf.String())
	return rec
}

func TestRedactorMasksByKeyWhateverTheValue(t *testing.T) {
	rec := redactedLine(t, func(l *slog.Logger) {
		l.Info("registering",
			"token", "plainvaluewithnoshape",
			"api_key", "plainvaluewithnoshape",
			"Authorization", "plainvaluewithnoshape",
			"password", "plainvaluewithnoshape",
			"cookie", "plainvaluewithnoshape",
			"client_secret", "plainvaluewithnoshape",
			"githubToken", "plainvaluewithnoshape",
			"gateway", "https://gw.example.com",
		)
	})
	for _, key := range []string{"token", "api_key", "Authorization", "password", "cookie", "client_secret", "githubToken"} {
		assert.Equal(t, redacted, rec[key], key)
	}
	assert.Equal(t, "https://gw.example.com", rec["gateway"])
}

// token_count and friends are measurements, not credentials, and an LLM-usage
// investigation needs them.
func TestRedactorKeepsTokenCounts(t *testing.T) {
	rec := redactedLine(t, func(l *slog.Logger) {
		l.Info("usage", "token_count", 1523, "max_tokens", 4096, "tokenizer", "cl100k", "tokens", 9)
	})
	assert.EqualValues(t, 1523, rec["token_count"])
	assert.EqualValues(t, 4096, rec["max_tokens"])
	assert.Equal(t, "cl100k", rec["tokenizer"])
	assert.EqualValues(t, 9, rec["tokens"])
}

func TestRedactorMasksMessageErrorsAndNestedGroups(t *testing.T) {
	err := errors.New("dial https://bot:hunter2hunter2@gw.example.com failed")
	rec := redactedLine(t, func(l *slog.Logger) {
		l.Error("auth with Bearer abcdef0123456789abcdef failed",
			"error", err,
			slog.Group("req", slog.String("url", "https://x.example.com/?token=abc123def456")),
		)
	})
	assert.NotContains(t, rec["msg"], "abcdef0123456789abcdef")
	assert.NotContains(t, rec["error"], "hunter2hunter2")
	assert.Contains(t, rec["error"], "gw.example.com", "host must survive")
	assert.NotContains(t, rec["req"].(map[string]any)["url"], "abc123def456")
}

func TestRedactorBoundsPayloadsAndMessages(t *testing.T) {
	rec := redactedLine(t, func(l *slog.Logger) {
		l.Info(strings.Repeat("m", 5000),
			"content", strings.Repeat("c", 5000),
			"body", []byte(strings.Repeat("z", 5000)),
			"err", errors.New(strings.Repeat("e", 5000)),
		)
	})
	assert.Less(t, len(rec["msg"].(string)), maxMessageBytes+30)
	assert.Less(t, len(rec["content"].(string)), maxAttrBytes+30)
	assert.Equal(t, "[5000 bytes]", rec["body"])
	assert.Less(t, len(rec["err"].(string)), maxErrorBytes+30)
}

func TestRedactorLeavesBuiltinsAndNumbersAlone(t *testing.T) {
	rec := redactedLine(t, func(l *slog.Logger) {
		l.Warn("slow", "elapsed_ms", 1234, "ok", true, "ratio", 0.5)
	})
	assert.Equal(t, "WARN", rec["level"])
	assert.NotEmpty(t, rec["time"])
	assert.EqualValues(t, 1234, rec["elapsed_ms"])
	assert.Equal(t, true, rec["ok"])
	assert.EqualValues(t, 0.5, rec["ratio"])
}

// The text handler (a person's terminal, LOG_FORMAT=text) goes through the same
// hook, so the same fixtures are masked there.
func TestRedactorAppliesToTextHandler(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: newRedactor().replaceAttr}))
	l.Info("hello", "token", "plainvaluewithnoshape", "url", "https://u:pw12345678@h.example.com/x")
	out := buf.String()
	assert.NotContains(t, out, "plainvaluewithnoshape")
	assert.NotContains(t, out, "pw12345678")
	assert.Contains(t, out, "h.example.com")
}
