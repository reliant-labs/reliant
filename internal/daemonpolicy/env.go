// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"context"
	"os"
	"strings"
)

// inheritableEnvPrefixes and inheritableEnvVars name the environment a confined
// caller's process may inherit from the daemon.
//
// The daemon's own environment is not a safe default to hand a connector. It
// holds the user's git credential (GIT_TOKEN, which the daemon uses to
// configure credential-store at startup), the gateway and server URLs, proxy
// settings, and whatever else the deployment injected. A connector that can
// run any allowlisted program can read all of it — `env`, `printenv`, a test
// that dumps its environment on failure, or simply a build script — and the
// output comes straight back through the tool result to a third-party model.
//
// So the child gets a constructed environment rather than a filtered copy of
// the daemon's. This is an allowlist for the same reason the request-supplied
// env check is: enumerating every secret a deployment might inject, forever,
// is not something anyone can do correctly, whereas enumerating what a build
// actually needs is a short and stable list.
//
// Unconfined (first-party) callers are unaffected and still inherit the full
// environment — that is the existing behavior, and the user's own agent is
// entitled to the user's own credentials.
var inheritableEnvVars = map[string]bool{
	// Process basics. Without PATH nothing resolves; without HOME many tools
	// write to / or fail outright.
	"PATH":    true,
	"HOME":    true,
	"USER":    true,
	"LOGNAME": true,
	"SHELL":   true,
	"TMPDIR":  true,
	"TMP":     true,
	"TEMP":    true,
	"PWD":     true,

	// Locale and terminal. Purely presentational, and their absence produces
	// mojibake and odd sorting in tool output.
	"LANG":     true,
	"LANGUAGE": true,
	"TERM":     true,
	"TZ":       true,

	// Toolchain locations a workspace image sets up. These point at
	// installations, not at credentials.
	"GOPATH":      true,
	"GOROOT":      true,
	"GOMODCACHE":  true,
	"GOCACHE":     true,
	"CARGO_HOME":  true,
	"RUSTUP_HOME": true,
	"JAVA_HOME":   true,
	"NVM_DIR":     true,
	"PYENV_ROOT":  true,

	// Marks the process as machine-driven, which makes many tools drop
	// interactive prompts and color codes.
	"CI":              true,
	"DEBIAN_FRONTEND": true,

	// Reliant's own marker, so scripts can detect the context. Carries no
	// secret.
	"RELIANT_SPAWNED": true,
}

// inheritableEnvPrefixes covers families that are safe as a group.
var inheritableEnvPrefixes = []string{
	// LC_* is the locale family (LC_ALL, LC_CTYPE, LC_NUMERIC, …).
	"LC_",
}

// ChildEnv returns the environment for a process spawned on behalf of the
// caller in ctx.
//
// For an unconfined caller it returns the daemon's full environment, unchanged.
// For a confined one it returns only the allowlisted subset, so a connector
// cannot read the user's git token or the deployment's internal URLs out of a
// command's own environment.
//
// extra is applied on top; callers are expected to have validated it (see
// Policy.checkExec), and it is not filtered again here.
func ChildEnv(ctx context.Context, extra map[string]string) []string {
	base := os.Environ()

	if FromContext(ctx) == nil {
		// First-party: unchanged behavior.
		return appendExtra(base, extra)
	}

	filtered := make([]string, 0, len(base))
	for _, kv := range base {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if envInheritable(name) {
			filtered = append(filtered, kv)
		}
	}
	return appendExtra(filtered, extra)
}

// envInheritable reports whether a variable may cross into a confined child.
func envInheritable(name string) bool {
	upper := strings.ToUpper(name)
	if inheritableEnvVars[upper] {
		return true
	}
	for _, prefix := range inheritableEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

func appendExtra(env []string, extra map[string]string) []string {
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}
