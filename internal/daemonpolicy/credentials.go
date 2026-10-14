// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// CredentialsNone is the shell's `credentials: none` mode: the child process
// is handed no GitHub, git, ssh or cloud credentials.
//
// This is NOT containment. It removes what Reliant and the daemon's own
// environment hand a child: tokens in the environment, the Reliant git
// credential helper, the ssh agent socket, gh's config directory. A child run
// as the same Unix user can still read that user's files (~/.config/gh/hosts.yml,
// ~/.git-credentials, ~/.ssh, the daemon's own credentials file) and no
// environment can stop it. Stopping that needs the shell to run as another user
// or in a namespace sandbox.
const (
	CredentialsInherit = ""
	CredentialsNone    = "none"
)

// ParseCredentials validates a `credentials` setting. "" and "inherit" are the
// default; "none" strips credentials.
func ParseCredentials(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "inherit":
		return CredentialsInherit, nil
	case CredentialsNone:
		return CredentialsNone, nil
	}
	return "", fmt.Errorf("credentials must be \"none\" or \"inherit\", got %q", s)
}

// credentialEnvNames are variables that carry a credential or say where one
// lives, removed whatever their value.
var credentialEnvNames = map[string]bool{
	"GIT_TOKEN":               true,
	"GITLAB_TOKEN":            true,
	"BITBUCKET_TOKEN":         true,
	"GIT_ASKPASS":             true,
	"SSH_ASKPASS":             true,
	"GIT_SSH":                 true,
	"GIT_SSH_COMMAND":         true,
	"SSH_AUTH_SOCK":           true,
	"SSH_AGENT_PID":           true,
	"KUBECONFIG":              true,
	"DOCKER_CONFIG":           true,
	"NPM_TOKEN":               true,
	"FORGE_TOKEN":             true,
	"FORGE_CREDENTIAL_HELPER": true,
	"RELIANT_TOKEN":           true,
}

// credentialEnvPrefixes remove whole families: gh's (GH_TOKEN, GH_HOST,
// GH_ENTERPRISE_TOKEN, GH_CONFIG_DIR), GitHub's, git's config injection
// (rebuilt below) and the major clouds'.
var credentialEnvPrefixes = []string{
	"GH_", "GITHUB_",
	"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_",
	"AWS_", "AZURE_", "GOOGLE_", "GCLOUD_", "GCP_", "CLOUDSDK_",
}

func isCredentialEnv(name string) bool {
	upper := strings.ToUpper(name)
	if credentialEnvNames[upper] {
		return true
	}
	for _, p := range credentialEnvPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	return false
}

// gitNeutralConfig resets every credential helper and askpass program git
// would otherwise consult: the URL-scoped helper Reliant installs for
// github.com, any unscoped helper (store, osxkeychain, manager) and
// core.askPass. An empty value resets a list, which is how git is told to
// forget what a lower-precedence config file set. It goes in through
// GIT_CONFIG_COUNT so the user's identity and aliases stay in place.
var gitNeutralConfig = [][2]string{
	{"credential.https://github.com.helper", ""},
	{"credential.helper", ""},
	{"core.askPass", ""},
}

var (
	emptyGHConfigOnce sync.Once
	emptyGHConfigDir  string
)

// neutralGHConfigDir is a path gh cannot read a login from, used when no empty
// directory could be made.
const neutralGHConfigDir = "/nonexistent/reliant-no-gh"

// emptyGHConfig is a read-only empty directory for GH_CONFIG_DIR, made once per
// process. Read-only so a `gh auth login` in a credentials-none shell has
// nowhere to leave a login behind for the next one.
func emptyGHConfig() string {
	emptyGHConfigOnce.Do(func() {
		dir, err := os.MkdirTemp("", "reliant-no-gh-")
		if err != nil {
			return
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			_ = os.Remove(dir)
			return
		}
		emptyGHConfigDir = dir
	})
	if emptyGHConfigDir == "" {
		return neutralGHConfigDir
	}
	return emptyGHConfigDir
}

// WithoutCredentials returns env with every credential variable removed, then
// the neutral settings appended: no prompts, no helpers, no ssh, an empty gh
// config directory. It removes whatever the caller's own env added too, so the
// result carries no credential variable at all.
func WithoutCredentials(env []string) []string {
	out := make([]string, 0, len(env)+12)
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || isCredentialEnv(name) {
			continue
		}
		out = append(out, kv)
	}
	out = append(out,
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never",
		"GH_PROMPT_DISABLED=1",
		"GH_CONFIG_DIR="+emptyGHConfig(),
		"GIT_CONFIG_COUNT="+strconv.Itoa(len(gitNeutralConfig)),
	)
	for i, kv := range gitNeutralConfig {
		out = append(out,
			"GIT_CONFIG_KEY_"+strconv.Itoa(i)+"="+kv[0],
			"GIT_CONFIG_VALUE_"+strconv.Itoa(i)+"="+kv[1])
	}
	if runtime.GOOS != "windows" {
		out = append(out, "GIT_SSH_COMMAND=/bin/false")
	}
	return out
}

// NeutralEnv is WithoutCredentials as assignments a caller can send in a
// request's env. A daemon that predates the request's `credentials` field
// ignores the field but applies env, so the common vectors (tokens, the git
// helper, ssh, the gh config) are neutralized there too instead of failing
// open. A current daemon builds the environment itself and strips everything
// it can name.
func NeutralEnv() map[string]string {
	m := map[string]string{
		"GH_TOKEN":            "",
		"GITHUB_TOKEN":        "",
		"GH_ENTERPRISE_TOKEN": "",
		"GIT_TERMINAL_PROMPT": "0",
		"GCM_INTERACTIVE":     "never",
		"GH_PROMPT_DISABLED":  "1",
		"GH_CONFIG_DIR":       neutralGHConfigDir,
		"SSH_AUTH_SOCK":       "",
		"GIT_ASKPASS":         "",
		"SSH_ASKPASS":         "",
		"KUBECONFIG":          "/dev/null",
		"GIT_CONFIG_COUNT":    strconv.Itoa(len(gitNeutralConfig)),
	}
	for i, kv := range gitNeutralConfig {
		m["GIT_CONFIG_KEY_"+strconv.Itoa(i)] = kv[0]
		m["GIT_CONFIG_VALUE_"+strconv.Itoa(i)] = kv[1]
	}
	if runtime.GOOS != "windows" {
		m["GIT_SSH_COMMAND"] = "/bin/false"
	}
	return m
}
