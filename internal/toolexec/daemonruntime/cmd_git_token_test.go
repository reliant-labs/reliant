// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testSecretToken = "ghu_TESTSECRET0123456789abcdefghijklmn"

func gitOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// A clone must leave a clean remote URL and no token anywhere in .git/config,
// even when an older control-plane still sends one in the payload.
func TestHandleGitClone_IgnoresLegacyTokenField(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs real git clone; skipped under -short")
	}
	setGitCloneTestEnv(t)
	t.Setenv("HOME", t.TempDir())
	origin := newTestOriginRepo(t)
	dest := filepath.Join(t.TempDir(), "dest")

	payload := []byte(`{"repo":` + jsonString(t, origin) + `,"branch":"main","path":` + jsonString(t, dest) + `,"token":"` + testSecretToken + `"}`)
	if _, err := handleGitClone(context.Background(), payload); err != nil {
		t.Fatalf("clone: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(dest, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), testSecretToken) || strings.Contains(string(cfg), "x-access-token") {
		t.Fatalf("token leaked into .git/config:\n%s", cfg)
	}
	if got := strings.TrimSpace(gitOut(t, "-C", dest, "remote", "get-url", "origin")); got != origin {
		t.Fatalf("origin url = %q, want the clean %q", got, origin)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".git-credentials")); err == nil {
		t.Fatal("clone wrote ~/.git-credentials")
	}
}

func TestGitRecloneRequest_IgnoresLegacyTokenField(t *testing.T) {
	var req gitRecloneRequest
	if err := json.Unmarshal([]byte(`{"path":"/p","repo":"r","token":"x"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Path != "/p" || req.Repo != "r" {
		t.Fatalf("req = %+v", req)
	}
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func withHelper(t *testing.T, argv ...string) {
	t.Helper()
	SetGitCredentialHelper(argv)
	t.Cleanup(func() { SetGitCredentialHelper(nil) })
}

// git itself, given the daemon's per-invocation env, runs the helper for
// github.com (with spaces in the path) and gets the answer.
func TestGitCredentialEnv_GitInvokesHelperForGithubOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs real git credential; skipped under -short")
	}
	setGitCloneTestEnv(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "my helper")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\necho username=x-access-token\necho \"password=$1-$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withHelper(t, script, "it's ok")

	run := func(host string, env []string) string {
		cmd := exec.Command("git", "credential", "fill")
		cmd.Env = env
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\n\n")
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	env := gitCredentialEnv(os.Environ())
	if got := run("github.com", env); !strings.Contains(got, "username=x-access-token") || !strings.Contains(got, "password=it's ok-get") {
		t.Fatalf("github.com not answered by helper:\n%s", got)
	}
	if got := run("example.com", env); strings.Contains(got, "x-access-token") {
		t.Fatalf("helper answered a non-github host:\n%s", got)
	}
}

func TestGitCredentialEnv_AppendsToExistingGitConfigEnv(t *testing.T) {
	withHelper(t, "/bin/h")
	env := gitCredentialEnv([]string{"A=1", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=c"})
	joined := strings.Join(env, "\n")
	for _, want := range []string{"GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_KEY_1=credential.https://github.com.helper", "GIT_CONFIG_VALUE_1=\n", "GIT_CONFIG_VALUE_2=!'/bin/h'"} {
		if !strings.Contains(joined+"\n", want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if strings.Count(joined, "GIT_CONFIG_COUNT=") != 1 {
		t.Error("duplicate GIT_CONFIG_COUNT")
	}
}

func TestGitCredentialHelperValue_QuotesSafely(t *testing.T) {
	got := gitCredentialHelperValue([]string{"/a b/reliant", "auth", "it's"})
	want := `!'/a b/reliant' 'auth' 'it'\''s'`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestWireGlobalGitCredentialHelper_IdempotentAndReplacesStore(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs real git config; skipped under -short")
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	gitOut(t, "config", "--global", "credential.helper", "store")
	argv := []string{"/x y/reliant", "auth", "git-credential", "--daemon-server", "http://s"}
	for i := 0; i < 2; i++ {
		if err := wireGlobalGitCredentialHelper(context.Background(), argv); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(cfg)
	if n := strings.Count(string(got), "helper ="); n != 1 {
		t.Fatalf("want exactly one helper entry, got %d:\n%s", n, got)
	}
	if strings.Contains(string(got), "= store") {
		t.Fatalf("old store helper left behind:\n%s", got)
	}
	// A user's own non-store global helper is untouched.
	gitOut(t, "config", "--global", "credential.helper", "osxkeychain")
	if err := wireGlobalGitCredentialHelper(context.Background(), argv); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, "config", "--global", "--get", "credential.helper"); strings.TrimSpace(got) != "osxkeychain" {
		t.Fatalf("user's helper clobbered: %q", got)
	}
}

// Off a managed daemon the global config is never written.
func TestSetupManagedGitCredentials_LocalDaemonLeavesGlobalConfigAlone(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv(DaemonTypeEnvVar, "")
	withHelper(t, "/bin/h")
	setupManagedGitCredentials()
	if _, err := os.Stat(cfg); err == nil {
		t.Fatal("a local daemon wrote the global git config")
	}
}

func TestHealLeakedGitCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs real git; skipped under -short")
	}
	setGitCloneTestEnv(t)
	home := t.TempDir()
	projects := t.TempDir()
	mk := func(name string, remotes map[string]string) string {
		d := filepath.Join(projects, name)
		gitOut(t, "init", "-q", d)
		for n, u := range remotes {
			gitOut(t, "-C", d, "remote", "add", n, u)
		}
		return d
	}
	leaked := mk("leaked", map[string]string{"origin": "https://x-access-token:" + testSecretToken + "@github.com/o/r.git"})
	clean := mk("clean", map[string]string{"origin": "https://github.com/o/c.git"})
	user := mk("user", map[string]string{"origin": "ssh://git@github.com/o/u.git", "up": "https://alice@github.com/o/u.git"})
	mk("none", nil)

	creds := filepath.Join(home, ".git-credentials")
	seed := "https://x-access-token:" + testSecretToken + "@github.com\nhttps://alice:pw@gitlab.com\nhttps://x-access-token:keep@gitlab.com\n"
	if err := os.WriteFile(creds, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	remotes, lines := healLeakedGitCredentials(context.Background(), projects, home)
	if remotes != 1 || lines != 1 {
		t.Fatalf("counts = %d, %d; want 1, 1", remotes, lines)
	}
	if got := strings.TrimSpace(gitOut(t, "-C", leaked, "remote", "get-url", "origin")); got != "https://github.com/o/r.git" {
		t.Fatalf("leaked url = %q", got)
	}
	if got := strings.TrimSpace(gitOut(t, "-C", clean, "remote", "get-url", "origin")); got != "https://github.com/o/c.git" {
		t.Fatalf("clean url = %q", got)
	}
	if got := strings.TrimSpace(gitOut(t, "-C", user, "remote", "get-url", "up")); got != "https://alice@github.com/o/u.git" {
		t.Fatalf("userinfo-without-password url = %q", got)
	}
	cfg, _ := os.ReadFile(filepath.Join(leaked, ".git", "config"))
	if strings.Contains(string(cfg), testSecretToken) {
		t.Fatal("token still in .git/config")
	}
	got, _ := os.ReadFile(creds)
	if string(got) != "https://alice:pw@gitlab.com\nhttps://x-access-token:keep@gitlab.com\n" {
		t.Fatalf("credentials file = %q", got)
	}
	if info, _ := os.Stat(creds); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}

	// Idempotent; and a file that held only the token goes away.
	if r, l := healLeakedGitCredentials(context.Background(), projects, home); r != 0 || l != 0 {
		t.Fatalf("second run counts = %d, %d", r, l)
	}
	if err := os.WriteFile(creds, []byte("https://x-access-token:"+testSecretToken+"@github.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	healLeakedGitCredentials(context.Background(), projects, home)
	if _, err := os.Stat(creds); !os.IsNotExist(err) {
		t.Fatal("empty credentials file should be removed")
	}
}

func TestStripURLPassword(t *testing.T) {
	for in, want := range map[string]string{
		"https://x-access-token:T@github.com/o/r.git": "https://github.com/o/r.git",
		"https://github.com/o/r.git":                  "",
		"https://alice@github.com/o/r.git":            "",
		"git@github.com:o/r.git":                      "",
		"/local/path":                                 "",
	} {
		got, leaked := stripURLPassword(in)
		if got != want || leaked != (want != "") {
			t.Errorf("%q -> %q,%v", in, got, leaked)
		}
	}
}
