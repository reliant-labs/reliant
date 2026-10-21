// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
)

// credentialedEnv puts every credential a Reliant machine hands a shell into
// the process environment, as the daemon would have it.
func credentialedEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"GH_TOKEN":                "ghp_exec_gh_token",
		"GITHUB_TOKEN":            "ghp_exec_github_token",
		"GIT_TOKEN":               "ghp_exec_git_token",
		"SSH_AUTH_SOCK":           "/tmp/exec-agent.sock",
		"FORGE_CREDENTIAL_HELPER": `["reliant","auth","forge-credential"]`,
		"AWS_SECRET_ACCESS_KEY":   "exec-aws",
		"KUBECONFIG":              "/home/u/.kube/config",
	} {
		t.Setenv(k, v)
	}
}

func TestExecRun_CredentialsNone_ChildEnvironmentHasNoCredentials(t *testing.T) {
	credentialedEnv(t)

	res := runExec(t, daemon.RunCommandRequest{Command: "env", WorkingDir: t.TempDir(), Credentials: "none",
		Env: map[string]string{"GH_TOKEN": "ghp_from_request"}})
	if res.ExitCode != 0 {
		t.Fatalf("env failed: %+v", res)
	}
	assertNoCredentials(t, res.Stdout)

	// And without the setting, nothing changed.
	res = runExec(t, daemon.RunCommandRequest{Command: "env", WorkingDir: t.TempDir()})
	if !strings.Contains(res.Stdout, "GH_TOKEN=ghp_exec_gh_token") {
		t.Fatal("the default environment must still carry what the daemon has")
	}
}

func TestExecRun_CredentialsNone_TypoFailsClosed(t *testing.T) {
	credentialedEnv(t)
	payload, _ := json.Marshal(daemon.RunCommandRequest{Command: "env", WorkingDir: t.TempDir(), Credentials: "nne"})
	if _, err := handleExecRun(context.Background(), payload); err == nil {
		t.Fatal("an unknown credentials value must refuse the command, not run it with credentials")
	}
}

func TestExecBGStart_CredentialsNone_ChildEnvironmentHasNoCredentials(t *testing.T) {
	credentialedEnv(t)
	payload, _ := json.Marshal(daemon.RunCommandRequest{Command: "env", WorkingDir: t.TempDir(), Credentials: "none"})
	raw, err := handleExecBGStart(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		ProcessID string `json:"process_id"`
	}
	if err := json.Unmarshal(raw, &started); err != nil {
		t.Fatal(err)
	}
	// Read the output once the process has finished, not as soon as some of
	// it is there: a partial `env` listing can lack the very lines asserted.
	mgr := shell.GetBackgroundManager()
	deadline := time.Now().Add(20 * time.Second)
	for {
		status, complete, _, err := mgr.GetProcessStatus(started.ProcessID)
		if err != nil {
			t.Fatal(err)
		}
		if complete {
			if status != "completed" {
				t.Fatalf("env finished with status %q", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("env still %q after 20s", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	lines, err := mgr.GetCombinedOutput(started.ProcessID)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(l.Text + "\n")
	}
	assertNoCredentials(t, sb.String())
}

func assertNoCredentials(t *testing.T, envOutput string) {
	t.Helper()
	for _, secret := range []string{"ghp_exec", "ghp_from_request", "exec-aws", "exec-agent.sock", "forge-credential"} {
		if strings.Contains(envOutput, secret) {
			t.Errorf("credential %q is in the child's environment:\n%s", secret, envOutput)
		}
	}
	for _, name := range []string{"GH_TOKEN=", "GITHUB_TOKEN=", "GIT_TOKEN=", "SSH_AUTH_SOCK=", "KUBECONFIG=", "AWS_SECRET_ACCESS_KEY="} {
		for _, line := range strings.Split(envOutput, "\n") {
			if strings.HasPrefix(line, name) {
				t.Errorf("%s is set in the child's environment", strings.TrimSuffix(name, "="))
			}
		}
	}
	for _, want := range []string{"GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1", "GH_CONFIG_DIR="} {
		if !strings.Contains(envOutput, want) {
			t.Errorf("%s is missing from the child's environment", want)
		}
	}
}

// authServer is a git smart-HTTP endpoint that demands HTTP authentication, and
// records every Authorization header it was sent. Whatever answers an
// authenticated request is a refusal: the test is about whether credentials
// REACH it, never about a push succeeding.
type authServer struct {
	*httptest.Server
	mu   sync.Mutex
	auth []string
}

func newAuthServer(t *testing.T) *authServer {
	t.Helper()
	s := &authServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *authServer) authorizations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, a := range s.auth {
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}

// A shell with credentials: none cannot push to a remote that wants
// credentials, and it fails fast with no prompt, even when the machine has every
// credential source git knows about: an askpass program, a credential helper in
// the user's config, a token in the environment.
func TestGitPushFromCredentialsNoneShell_FailsWithNoPrompt(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: spawns git against a local HTTP server; runs in the full lane")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	srv := newAuthServer(t)

	home := os.Getenv("HOME")
	askpass := filepath.Join(home, "askpass.sh")
	if err := os.WriteFile(askpass, []byte("#!/bin/sh\necho hunter2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitconfig := os.Getenv("GIT_CONFIG_GLOBAL")
	helper := "[credential]\n\thelper = \"!f() { echo username=bot; echo password=hunter2; }; f\"\n"
	// Appended: the file is TestMain's shared config, and later tests still
	// need what it already holds (noAutoMaintenanceConfig).
	cfg, err := os.OpenFile(gitconfig, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.WriteString(helper); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_ASKPASS", askpass)
	credentialedEnv(t)

	work := t.TempDir()
	setup := "git init -q -b feat . && git -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m init && " +
		"git remote add origin " + srv.URL + "/o/r.git"
	if res := runExec(t, daemon.RunCommandRequest{Command: setup, WorkingDir: work, Credentials: "none"}); res.ExitCode != 0 {
		t.Fatalf("setup: %+v", res)
	}

	start := time.Now()
	res := runExec(t, daemon.RunCommandRequest{Command: "git push origin HEAD:refs/heads/feat", WorkingDir: work,
		Credentials: "none", TimeoutMs: 30000})
	if res.ExitCode == 0 {
		t.Fatalf("git push succeeded from a credentials-none shell: %+v", res)
	}
	if res.TimedOut || time.Since(start) > 20*time.Second {
		t.Fatalf("git push hung (a prompt?): %+v", res)
	}
	if !strings.Contains(res.Combined, "terminal prompts disabled") && !strings.Contains(res.Combined, "could not read") &&
		!strings.Contains(res.Combined, "Authentication failed") {
		t.Errorf("git push failed, but not for want of credentials: %q", res.Combined)
	}
	if got := srv.authorizations(); len(got) != 0 {
		t.Fatalf("credentials reached the remote from a credentials-none shell: %v", got)
	}

	// Control: the same machine, same repo, WITHOUT the setting does present
	// credentials. Without this the assertion above could pass because the
	// server, the helper or the askpass never worked at all.
	res = runExec(t, daemon.RunCommandRequest{Command: "git push origin HEAD:refs/heads/feat", WorkingDir: work, TimeoutMs: 30000})
	if res.ExitCode == 0 {
		t.Fatalf("control push unexpectedly succeeded against a refusing server: %+v", res)
	}
	if got := srv.authorizations(); len(got) == 0 {
		t.Fatalf("control: an inheriting shell sent no credentials either, so this test proves nothing: %q", res.Combined)
	}
}
