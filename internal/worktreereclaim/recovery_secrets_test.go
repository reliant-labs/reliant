// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestRecoverySecretPolicy(t *testing.T) {
	for _, tt := range []struct {
		name string
		body []byte
		hold bool
	}{
		{"ordinary source", []byte("package example\nfunc sum(a,b int) int { return a+b }\n"), false},
		{"private key", []byte("-----BEGIN RSA PRIVATE KEY-----\n"), true},
		{"assignment", []byte("API_KEY=not-a-real-credential\n"), true},
		{"provider token", []byte("ghp_" + strings.Repeat("a", 36)), true},
		{"binary", []byte{0, 1, 2}, true},
		{"non utf8", []byte{0xff, 0xfe}, true},
		{"encrypted opaque", []byte("mJ2c+PX0uQi9Ak3rYwDb5slT/Vhx8fEn"), true},
		{"lfs", []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\n"), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := recoveryScanContent(tt.body)
			if (err != nil) != tt.hold {
				t.Fatalf("hold=%v error=%v", tt.hold, err)
			}
		})
	}
	// This detector is not perfect: arbitrary disguised credentials are not
	// mathematically distinguishable from ordinary text. Never advertise otherwise.
	if err := recoveryScanContent([]byte("the red apple falls")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".env", "dist/.env.production", "node_modules/credentials.json", "data/state.sqlite", "archive.zip", "key.pem"} {
		if recoveryScanPath([]byte(p)) == nil {
			t.Errorf("sensitive path allowed: %s", p)
		}
	}
}

func TestRecoveryRawPathParsing(t *testing.T) {
	oid := strings.Repeat("a", 40)
	names := []string{"new\nline", "not-utf8-\xff", "tab\tname"}
	var raw strings.Builder
	for _, n := range names {
		raw.WriteString("100644 blob " + oid + "\t" + n + "\x00")
	}
	entries, err := recoveryEntries([]byte(raw.String()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		p, err := base64.StdEncoding.DecodeString(e.Path)
		if err != nil {
			t.Fatal(err)
		}
		got[string(p)] = true
	}
	for _, n := range names {
		if !got[n] {
			t.Errorf("lost raw path %q", n)
		}
	}
	for _, p := range []string{"../outside", "/absolute", "a/../outside", "a/.GIT/config", "a\\b", "a/", "C:drive", "a. "} {
		if recoveryPath(p) == nil {
			t.Errorf("unsafe path accepted %q", p)
		}
	}
	if _, err := recoveryEntries([]byte("100644 blob " + oid + "\tCase\x00100644 blob " + oid + "\tcase\x00")); err == nil {
		t.Fatal("case collision accepted")
	}
}
