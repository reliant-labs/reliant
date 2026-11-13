// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// CredentialsNone is the shell's `credentials: none` mode: the child process
// is handed no GitHub, git, ssh or cloud credentials.
//
// It has two halves. This package strips the environment: tokens, the Reliant
// git credential helper, the ssh agent socket, gh's config directory. That alone
// is not containment: a child run as the same Unix user can read that user's
// files (~/.config/gh/hosts.yml, ~/.git-credentials, ~/.ssh, the daemon's own
// credentials file). The other half is internal/credsandbox, which runs the
// child in user, mount and PID namespaces where ProtectedCredentials are absent
// (on Linux; elsewhere only the environment half applies).
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
	"RELIANT_FORGE_ORG":       true,
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

// secretEnvName matches a variable name that says it holds a secret or says
// where one is: OPENAI_API_KEY, STRIPE_SECRET, RELIANT_GITHUB_WEBHOOK_SECRET,
// DATABASE_URL (a DSN carries its password), SESSION_COOKIE.
var secretEnvName = regexp.MustCompile(`(?i)(token|secret|passw(or)?d|credential|key|dsn|cookie|session|authorization|_ur[li]$)`)

// secretEnvValues are forge/pkg/redact's exact credential shapes: URL userinfo
// (a proxy or GOPROXY with a password), a PEM private key, a JWT, and the
// provider key prefixes. Its two heuristic rules (a 40-character mixed-case
// run, any run of 40+ word characters) are left out on purpose: a value here
// is a path more often than not, and a long directory name in HOME or PATH
// would match them and leave the command with no HOME and no PATH.
var secretEnvValues = []*regexp.Regexp{
	regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.\-]*://[^\s/@:]+:[^\s/@]+@`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]*`),
	regexp.MustCompile(`\b(sk-ant-[A-Za-z0-9_\-]{8,}|sk-[A-Za-z0-9_\-]{16,}|sk_(?:live|test)_[A-Za-z0-9]{8,}|(?:rlat|dpat|rcl|gho|ghp|ghu|ghs|ghr|github_pat|glpat|xox[abprs])[_\-][A-Za-z0-9_\-]{10,}|AIza[0-9A-Za-z_\-]{20,}|ya29\.[0-9A-Za-z_\-]+|AKIA[0-9A-Z]{16})`),
}

// looksSecret reports whether a variable is a secret by its name, or by its
// value in one of the exact shapes forge/pkg/redact recognises.
func looksSecret(name, value string) bool {
	if secretEnvName.MatchString(name) {
		return true
	}
	for _, re := range secretEnvValues {
		if re.MatchString(value) {
			return true
		}
	}
	return false
}

// StripCredentials returns the environment for a process that must hold no
// credential: inherited (normally the daemon's own environment) reduced to the
// variables a build needs and none that looks like a secret, then extra (what
// the request set explicitly) without its credential variables, then the
// neutral non-git settings: no prompts, no ssh, an empty gh config directory.
//
// inherited is an allowlist (envInheritable), not a deny list. A deny list of
// credential names was the earlier design and it leaked whatever it did not
// name: OPENAI_API_KEY, DATABASE_URL, a *_WEBHOOK_SECRET all reached a
// credentials-none shell, which shares the network, so a prompt injection
// could send them out (the 2026-11-01 review, RF-4). What a build needs is a
// short, stable list; every secret a host might hold is not.
//
// It leaves git's own configuration to the caller, which is what a caller that
// supplies its own credential helper needs: GIT_CONFIG_COUNT entries are read
// AFTER `git -c`, so the resets in WithoutCredentials would wipe it.
func StripCredentials(inherited []string, extra map[string]string) []string {
	out := make([]string, 0, len(inherited)+len(extra)+12)
	for _, kv := range inherited {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !envInheritable(name) || isCredentialEnv(name) || looksSecret(name, value) {
			continue
		}
		out = append(out, kv)
	}
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if isCredentialEnv(name) || looksSecret(name, extra[name]) {
			continue
		}
		out = append(out, name+"="+extra[name])
	}
	out = append(out,
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never",
		"GH_PROMPT_DISABLED=1",
		"GH_CONFIG_DIR="+emptyGHConfig(),
	)
	if runtime.GOOS != "windows" {
		out = append(out, "GIT_SSH_COMMAND=/bin/false")
	}
	return out
}

// WithoutCredentials is StripCredentials plus git's credential helpers and
// askpass program reset, so a `git push` fails to authenticate and returns at
// once. It removes whatever the caller's own env added too, so the result carries
// no credential variable at all.
func WithoutCredentials(inherited []string, extra map[string]string) []string {
	out := StripCredentials(inherited, extra)
	out = append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(len(gitNeutralConfig)))
	for i, kv := range gitNeutralConfig {
		out = append(out,
			"GIT_CONFIG_KEY_"+strconv.Itoa(i)+"="+kv[0],
			"GIT_CONFIG_VALUE_"+strconv.Itoa(i)+"="+kv[1])
	}
	return out
}

// NeutralizeEnv is own without a credential, with NeutralEnv laid over it.
// A variable of own that carries or locates a credential (by name: a token,
// a key, a cloud or Reliant credential; or by value: a provider key, a JWT,
// URL userinfo) is dropped, exactly as StripCredentials drops it from a
// request's env, and every neutral assignment wins over a value own gives the
// same name. So a caller's (or a model's) env cannot hand a command a
// credential even on a daemon that predates `credentials: none` and applies
// env alone; own's other variables pass through.
func NeutralizeEnv(own map[string]string) map[string]string {
	env := NeutralEnv()
	for k, v := range own {
		if _, neutral := env[k]; neutral || isCredentialEnv(k) || looksSecret(k, v) {
			continue
		}
		env[k] = v
	}
	return env
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
