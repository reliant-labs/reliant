// Copyright (c) 2025 Reliant Labs
package daemonpolicy

import (
	"context"
	"os"
	"strings"
	"testing"
)

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// credentialSetup is every credential variable a managed or developer machine
// plausibly has, plus the ones a workflow author might add by hand.
var credentialSetup = map[string]string{
	"GH_TOKEN":                       "ghp_gh",
	"GITHUB_TOKEN":                   "ghp_github",
	"GH_ENTERPRISE_TOKEN":            "ghe",
	"GH_HOST":                        "github.example.com",
	"GITHUB_PAT":                     "pat",
	"GIT_TOKEN":                      "gt",
	"GIT_ASKPASS":                    "/usr/bin/askpass",
	"SSH_ASKPASS":                    "/usr/bin/ssh-askpass",
	"GIT_SSH_COMMAND":                "ssh -i /home/u/.ssh/id",
	"SSH_AUTH_SOCK":                  "/tmp/ssh-agent.sock",
	"SSH_AGENT_PID":                  "42",
	"FORGE_CREDENTIAL_HELPER":        `["reliant","auth","forge-credential"]`,
	"AWS_SECRET_ACCESS_KEY":          "aws",
	"AWS_SESSION_TOKEN":              "aws",
	"GOOGLE_APPLICATION_CREDENTIALS": "/home/u/gcp.json",
	"CLOUDSDK_AUTH_ACCESS_TOKEN":     "gcloud",
	"AZURE_CLIENT_SECRET":            "az",
	"KUBECONFIG":                     "/home/u/.kube/config",
	"GIT_CONFIG_COUNT":               "1",
	"GIT_CONFIG_KEY_0":               "credential.https://github.com.helper",
	"GIT_CONFIG_VALUE_0":             "!/opt/reliant auth git-credential",
}

func TestCredentialsNone_RemovesEveryCredentialVariable(t *testing.T) {
	for k, v := range credentialSetup {
		t.Setenv(k, v)
	}
	t.Setenv("HOME", "/home/u")
	t.Setenv("GOPATH", "/home/u/go")

	// The caller's own env cannot put one back.
	extra := map[string]string{"GH_TOKEN": "ghp_from_request", "MY_VAR": "kept"}
	env := envMap(ChildEnv(context.Background(), extra, CredentialsNone))

	for name := range credentialSetup {
		switch name {
		case "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_SSH_COMMAND":
			continue // rebuilt as neutral values, asserted below
		}
		if v, ok := env[name]; ok {
			t.Errorf("%s=%q survived credentials: none", name, v)
		}
	}
	if v, ok := env["GH_TOKEN"]; ok {
		t.Errorf("GH_TOKEN=%q from the request survived", v)
	}
	for k, v := range env {
		if strings.Contains(v, "ghp_") {
			t.Errorf("a token value is still in the environment under %s", k)
		}
	}

	if env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want 0", env["GIT_TERMINAL_PROMPT"])
	}
	if env["GH_PROMPT_DISABLED"] != "1" {
		t.Errorf("GH_PROMPT_DISABLED = %q, want 1", env["GH_PROMPT_DISABLED"])
	}
	dir := env["GH_CONFIG_DIR"]
	if dir == "" || dir == "/home/u/.config/gh" {
		t.Fatalf("GH_CONFIG_DIR = %q, want an empty directory of ours", dir)
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) != 0 {
		t.Errorf("GH_CONFIG_DIR %s is not empty: %v", dir, entries)
	}

	// Every credential helper git would consult is reset, and nothing else is
	// injected: the neutral config is exactly the three resets.
	if env["GIT_CONFIG_COUNT"] != "3" {
		t.Fatalf("GIT_CONFIG_COUNT = %q, want 3", env["GIT_CONFIG_COUNT"])
	}
	resets := map[string]bool{}
	for _, i := range []string{"0", "1", "2"} {
		if v := env["GIT_CONFIG_VALUE_"+i]; v != "" {
			t.Errorf("GIT_CONFIG_VALUE_%s = %q, want empty (a reset)", i, v)
		}
		resets[env["GIT_CONFIG_KEY_"+i]] = true
	}
	for _, key := range []string{"credential.https://github.com.helper", "credential.helper", "core.askPass"} {
		if !resets[key] {
			t.Errorf("git config %s is not reset", key)
		}
	}

	// What a build needs still arrives.
	if env["HOME"] != "/home/u" || env["GOPATH"] != "/home/u/go" || env["MY_VAR"] != "kept" {
		t.Errorf("ordinary variables were dropped: HOME=%q GOPATH=%q MY_VAR=%q", env["HOME"], env["GOPATH"], env["MY_VAR"])
	}
}

func TestCredentialsInherit_KeepsTheEnvironment(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_inherited")
	env := envMap(ChildEnv(context.Background(), nil, CredentialsInherit))
	if env["GH_TOKEN"] != "ghp_inherited" {
		t.Fatalf("default behaviour changed: GH_TOKEN = %q", env["GH_TOKEN"])
	}
}

func TestCredentialsNone_AlsoAppliesToAConfinedCaller(t *testing.T) {
	t.Setenv("GH_TOKEN", "x")
	ctx := NewContext(context.Background(), &Policy{GrantID: "g"})
	if _, ok := envMap(ChildEnv(ctx, nil, CredentialsNone))["GH_TOKEN"]; ok {
		t.Fatal("GH_TOKEN reached a credentials-none child")
	}
}

func TestParseCredentials(t *testing.T) {
	for in, want := range map[string]string{"": "", " none ": "none", "NONE": "none", "inherit": ""} {
		got, err := ParseCredentials(in)
		if err != nil || got != want {
			t.Errorf("ParseCredentials(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseCredentials("nne"); err == nil {
		t.Error("a typo must be an error: it must not silently keep the credentials")
	}
}

// A daemon older than the `credentials` field ignores it but applies env, so the
// neutral env must neutralize the same vectors on its own.
func TestNeutralEnv_CoversTheCommonVectors(t *testing.T) {
	env := NeutralEnv()
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "GIT_ASKPASS"} {
		if v, ok := env[k]; !ok || v != "" {
			t.Errorf("%s = %q (present=%v), want an explicit empty value", k, v, ok)
		}
	}
	if env["GIT_TERMINAL_PROMPT"] != "0" || env["GIT_CONFIG_COUNT"] != "3" {
		t.Errorf("prompt/config not neutralized: %v", env)
	}
}

// credentials: none composes with the agent-shell marker: the `reliant` CLI
// still refuses to enable automations from such a shell, and a request cannot
// clear the marker by naming it.
func TestCredentialsNone_KeepsTheAgentShellMarker(t *testing.T) {
	for _, extra := range []map[string]string{nil, {AgentShellEnv: ""}} {
		env := envMap(ChildEnv(context.Background(), extra, CredentialsNone))
		if env[AgentShellEnv] != "1" {
			t.Errorf("extra=%v: %s = %q, want 1", extra, AgentShellEnv, env[AgentShellEnv])
		}
	}
	if isCredentialEnv(AgentShellEnv) {
		t.Errorf("%s must never be classified as a credential", AgentShellEnv)
	}
}
