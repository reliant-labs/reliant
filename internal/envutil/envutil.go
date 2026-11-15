// Package envutil holds the small, shared environment-handling helpers
// the build, deploy, and host-launch paths all need: a minimal .env
// parser and two env-overlay merges whose precedence is encoded in the
// name. Factoring them here ends a duplication where the same operation
// name (mergeEnv) meant opposite precedence in different packages.
package envutil

import (
	"os"
	"sort"
	"strings"
)

// ParseDotEnv parses a .env file (KEY=VALUE per line, # comments,
// trailing whitespace trimmed) into a map. Quoted values
// ("VALUE", 'VALUE') have their outer quotes stripped. An optional
// leading "export " is stripped. Missing file returns os.ErrNotExist
// (wrapped) so callers can treat absence as non-fatal.
//
// Intentionally minimal — we don't expand $VARS or `${VAR:-default}`
// shell features. Projects needing those should use direnv or a wrapper
// script; this helper is just enough for the common
// "DATABASE_URL=postgres://..." case the host-mode loop and the
// External deploy env_file overlay need.
func ParseDotEnv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// strip an optional leading "export ".
		line = strings.TrimPrefix(line, "export ")
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		// Strip a single layer of matching quotes, if present.
		if len(v) >= 2 {
			if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
		}
		out[k] = v
	}
	return out, nil
}

// MergeExtraWins layers extra KEY=VALUE pairs onto a base os.Environ()
// slice with extra winning on key conflict. Used where the overlay
// (env_file / BuildEnv) is meant to be authoritative for the variables
// it declares and the parent process's env is background context.
// Returns a fresh slice safe to assign to cmd.Env.
func MergeExtraWins(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return append([]string(nil), base...)
	}
	out := make([]string, 0, len(base)+len(extra))
	seen := map[string]struct{}{}
	for k, v := range extra {
		seen[k] = struct{}{}
		out = append(out, k+"="+v)
	}
	for _, kv := range base {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			out = append(out, kv)
			continue
		}
		if _, dup := seen[kv[:eq]]; dup {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// MergeBaseWins layers extra KEY=VALUE pairs onto a base os.Environ()
// slice with base winning on key conflict (so a developer's shell
// override always wins). Non-conflicting extras are appended. Returns a
// fresh slice safe to assign to cmd.Env.
func MergeBaseWins(base []string, extra map[string]string) []string {
	have := map[string]struct{}{}
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 {
			have[kv[:i]] = struct{}{}
		}
	}
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	for k, v := range extra {
		if _, exists := have[k]; exists {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

// EnvConflict records a key whose DECLARED value differed from the value
// already present in the parent environment.
type EnvConflict struct {
	Key      string
	Declared string
	Shell    string
	// ShellWon is true when the key was explicitly opted out of the
	// declared-wins rule, so the parent's value was kept.
	ShellWon bool
}

// MergeDeclaredWins layers extra KEY=VALUE pairs onto a base os.Environ()
// slice with the DECLARED value winning on conflict, and reports every
// conflict it resolved.
//
// This is the inverse of MergeBaseWins, and it is the right default for a
// declarative tool: a value the project states in its own configuration is a
// deliberate statement about how the process must run, while a matching name in
// the developer's shell is usually ambient and unrelated. Letting the ambient
// value win makes the declaration advisory — the project renders one config and
// the process runs with another, with nothing printed either way. forge already
// reached that conclusion for bind ports (see forceHostBindPorts) after the same
// class of desync; the failure is simply less visible for other keys. A real
// case: a TLS-intercepting proxy exports SSL_CERT_FILE into the shell, which in
// Go REPLACES the trust store, so every host child silently loses the roots its
// declared bundle supplied and outbound TLS fails for anything the proxy is not
// decrypting.
//
// shellWins names the keys that keep the old behaviour, for the deliberate
// ad-hoc override the previous default served by accident.
func MergeDeclaredWins(base []string, extra map[string]string, shellWins map[string]struct{}) ([]string, []EnvConflict) {
	baseVals := make(map[string]string, len(base))
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 {
			baseVals[kv[:i]] = kv[i+1:]
		}
	}

	var conflicts []EnvConflict
	keep := make(map[string]string, len(extra))
	for k, declared := range extra {
		shell, inShell := baseVals[k]
		if inShell && shell != declared {
			_, allowed := shellWins[k]
			if ShellAlwaysWins(k) {
				allowed = true
			}
			conflicts = append(conflicts, EnvConflict{Key: k, Declared: declared, Shell: shell, ShellWon: allowed})
			if allowed {
				continue
			}
		}
		keep[k] = declared
	}

	out := make([]string, 0, len(base)+len(keep))
	for _, kv := range base {
		i := strings.IndexByte(kv, '=')
		if i > 0 {
			if _, overridden := keep[kv[:i]]; overridden {
				continue
			}
		}
		out = append(out, kv)
	}
	for k, v := range keep {
		out = append(out, k+"="+v)
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Key < conflicts[j].Key })
	return out, conflicts
}

// ParseShellWins reads the opt-out list (a comma-separated set of variable
// names) that lets the parent shell keep winning for specific keys.
func ParseShellWins(raw string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}

// Lookup returns the value of key in a KEY=VALUE slice, or "" when absent.
func Lookup(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

// osIdentityEnv names variables that describe the MACHINE and the session, not
// the application: the shell keeps these even under declared-wins. A project
// that names PATH almost never means "replace the interpreter search path with
// this literal string" — KCL cannot expand $PATH, so a declared value would be
// absolute and would strip the toolchain the process needs to exec at all.
// These are also read by the OS and by forge itself, not just by the app, which
// is the same reason bind ports are forced the other way.
//
// HOME IS NOT ON THIS LIST, deliberately. Unlike PATH, a declared HOME is a
// complete value — an absolute directory KCL can state exactly — and declaring
// it is never an accident: it is how a workload is given a sandboxed home, so
// the files its tools write under ~ (a CLI's credentials, git config, caches)
// land somewhere the environment owns instead of in the developer's own
// home. With HOME here, that declaration was silently replaced by the shell's
// and the process wrote into the real ~ — the opposite of what was declared.
// An env that does not declare HOME still inherits the shell's, as before.
var osIdentityEnv = map[string]struct{}{
	"PATH": {}, "USER": {}, "LOGNAME": {}, "SHELL": {},
	"TMPDIR": {}, "PWD": {}, "OLDPWD": {}, "TERM": {}, "SSH_AUTH_SOCK": {},
}

// ShellAlwaysWins reports whether the parent environment keeps this key
// regardless of what the project declares.
func ShellAlwaysWins(key string) bool {
	_, ok := osIdentityEnv[key]
	return ok
}
