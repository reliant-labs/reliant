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

// These synthetic tokens are deliberately caught even with harmless file
// contents. Encoding a path into manifest JSON must not bypass the detector.
func TestRecoveryCredentialFilenameGate(t *testing.T) {
	token := "ghp_" + strings.Repeat("x", 36)
	for _, name := range []string{token, "src/" + token + ".txt", token + "/safe.txt", "prefix-\xff" + token} {
		if err := recoveryScanPath([]byte(name)); err == nil {
			t.Fatal("credential-bearing decoded filename accepted")
		}
	}
	oid := strings.Repeat("a", 40)
	m := RecoveryManifest{Version: RecoveryFormatVersion, SecretPolicy: RecoverySecretPolicy, CaptureID: "capture", WorktreeID: "worktree", RepoID: "repo", Rel: ".", HeadOID: oid, IndexTreeOID: oid, DiskTreeOID: oid, ObjectFormat: "sha1", HistoryTips: []string{oid}, OmittedDigest: recoveryDigest([]RecoveryEntry{})}
	encoded := base64.StdEncoding.EncodeToString([]byte(token))
	m.DiskEntries = []RecoveryEntry{{Path: encoded, Mode: "100644", OID: oid}}
	m.UntrackedPaths = []string{encoded}
	m.InventoryDigest = recoveryDigest(recoveryManifestInventory(m))
	if err := recoveryManifestCheck(m); err == nil {
		t.Fatal("base64 manifest filename bypassed credential detector")
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
