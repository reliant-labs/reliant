// Copyright (c) 2025 Reliant Labs
//
// Package daemonpolicy confines what a caller may do on a daemon.
//
// It exists for connector callers: third-party MCP clients (ChatGPT, Claude)
// driving a cloud daemon on a user's behalf. Those callers are steered by a
// model reading untrusted text, so unlike the first-party app they cannot be
// handed the daemon's full command surface.
//
// The enforcement point is the daemon's command dispatch, which every command
// passes through. Deciding here rather than in the MCP server is deliberate:
// anything that learns to speak the daemon protocol is covered, not just
// today's one caller.
//
// A nil Policy means unrestricted. First-party callers attach none and are
// unaffected; confinement is opt-in, carried per-request in the context.
//
// Bash is the honest limit of this layer. A shell can reach any path the
// process can, so PathRoot binds the fs.* commands but cannot bind a shell
// command's reach. Containment for exec.run is the pod boundary, not this
// package. See Policy.ExecMode.
package daemonpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrDenied is the sentinel for every policy rejection. Callers map it to an
// MCP error or a Connect PermissionDenied; the wrapped text names the reason.
var ErrDenied = errors.New("denied by connector policy")

// ExecMode controls access to shell execution (exec.run and the exec.bg_*
// family).
type ExecMode string

const (
	// ExecDenied blocks shell execution outright. The default: a zero-value
	// Policy is the most restrictive one, so a construction bug fails closed.
	ExecDenied ExecMode = ""

	// ExecAllowlist permits only commands whose first word appears in
	// ExecAllowlist. This stops a model from casually running something
	// destructive. It is NOT a security boundary against a determined
	// injection — `git` alone reaches arbitrary code via `git -c
	// core.pager=...`, and any allowed interpreter reaches everything. Treat
	// it as a guard rail, with the container as the actual boundary.
	ExecAllowlist ExecMode = "allowlist"

	// ExecUnrestricted permits any shell command. Only meaningful when the
	// daemon is a disposable per-user sandbox, which is what cloud workspaces
	// are. Never grant this to a daemon running on a user's own machine.
	ExecUnrestricted ExecMode = "unrestricted"
)

// Policy is one connector grant, resolved for a single request.
//
// The zero value denies everything, so a partially-populated Policy is safe
// rather than accidentally permissive.
type Policy struct {
	// GrantID identifies the grant this policy came from, for audit records.
	GrantID string

	// Tools is the set of permitted daemon command types (e.g. "fs.read_file").
	// Empty denies all commands. Membership is exact; there is no wildcard, so
	// a command added to the daemon later is denied until a grant names it.
	Tools map[string]bool

	// PathRoot confines the fs.* commands to a directory subtree. Empty means
	// no filesystem access is granted at all — not "unconfined" — so that
	// forgetting to set it cannot open the whole disk.
	PathRoot string

	// ExecMode selects the shell-execution rule. See the ExecMode constants.
	ExecMode ExecMode

	// ExecAllowlist holds permitted command basenames when ExecMode is
	// ExecAllowlist (e.g. "git", "go", "npm"). Ignored in other modes.
	ExecAllowlist map[string]bool

	// ExpiresAt bounds the grant's lifetime. Zero means no expiry.
	ExpiresAt time.Time
}

type policyKey struct{}

// NewContext returns a context carrying p. A nil p yields an unrestricted
// context, which is what first-party callers use.
func NewContext(ctx context.Context, p *Policy) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, policyKey{}, p)
}

// FromContext returns the policy in ctx, or nil when the caller is
// unrestricted.
func FromContext(ctx context.Context) *Policy {
	p, _ := ctx.Value(policyKey{}).(*Policy)
	return p
}

// GrantIDFromContext returns the grant id confining this request, or "" when
// the caller is unrestricted. Handlers use it to tag and scope resources that
// outlive a single command, such as background processes.
func GrantIDFromContext(ctx context.Context) string {
	if p := FromContext(ctx); p != nil {
		return p.GrantID
	}
	return ""
}

// GrantIDForLog returns the grant identifier for log lines, or "none" when the
// caller is unrestricted. It is nil-safe so logging never needs a guard.
func (p *Policy) GrantIDForLog() string {
	if p == nil {
		return "none"
	}
	return p.GrantID
}

// Check reports whether commandType with the given JSON payload is permitted.
// It returns an error wrapping ErrDenied when it is not.
//
// A nil policy permits everything: this is the first-party path, and it must
// stay exactly as fast and permissive as it was before this package existed.
func (p *Policy) Check(commandType string, payload []byte) error {
	if p == nil {
		return nil
	}

	if !p.ExpiresAt.IsZero() && time.Now().After(p.ExpiresAt) {
		return fmt.Errorf("%w: grant expired at %s", ErrDenied, p.ExpiresAt.Format(time.RFC3339))
	}

	if !p.Tools[commandType] {
		return fmt.Errorf("%w: command %q is not in this connector's allowed tools", ErrDenied, commandType)
	}

	if isExecCommand(commandType) {
		if err := p.checkExec(payload); err != nil {
			return err
		}
	}

	// Path confinement runs for every command, not just fs.*. Commands outside
	// the fs family still carry paths (repo.discover, terminal.create's
	// working_dir), and they must land inside the root too.
	return p.checkPaths(payload)
}

// isExecCommand reports whether commandType starts a process whose command
// line the caller controls.
func isExecCommand(commandType string) bool {
	switch commandType {
	case "exec.run", "exec.bg_start":
		return true
	default:
		// terminal.create spawns an interactive shell, which is unrestricted
		// execution by another name. It is gated by the tool allowlist alone;
		// a grant that includes it is granting shell access.
		return false
	}
}

// checkExec applies ExecMode to a payload's command field.
func (p *Policy) checkExec(payload []byte) error {
	switch p.ExecMode {
	case ExecUnrestricted:
		return nil
	case ExecDenied:
		return fmt.Errorf("%w: shell execution is not granted to this connector", ErrDenied)
	case ExecAllowlist:
		// fall through
	default:
		// An unrecognized mode is a bug in grant construction. Fail closed.
		return fmt.Errorf("%w: unknown exec mode %q", ErrDenied, p.ExecMode)
	}

	// Under an allowlist the command MUST arrive as argv. This is the whole
	// mechanism, not a formatting preference.
	//
	// A shell string cannot be checked meaningfully: whatever this function
	// concludes about it, `bash -c` re-parses it afterward, and quoting,
	// expansion, aliases, config flags, and inherited environment each reopen
	// what the check tried to close. The previous approach here — take the
	// first word, reject metacharacters, reject env prefixes — was a denylist
	// racing an interpreter, and it lost: `PATH=/planted git status` ran a
	// planted binary while passing every check.
	//
	// With argv there is no interpreter. The string compared against the
	// allowlist is the string handed to execve.
	argv, err := extractArgv(payload)
	if err != nil {
		return err
	}
	if len(argv) == 0 {
		return fmt.Errorf(
			"%w: this connector may only run specific commands, so the request must supply the command "+
				"and its arguments separately rather than as one shell string", ErrDenied)
	}

	base := argv[0]
	if strings.TrimSpace(base) == "" {
		return fmt.Errorf("%w: no command was supplied", ErrDenied)
	}
	// A directory part is stripped so "/usr/bin/git" and "git" compare equal,
	// matching what a user writing an allowlist means.
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if !p.ExecAllowlist[base] {
		return fmt.Errorf("%w: command %q is not in this connector's allowed commands", ErrDenied, base)
	}

	// Even without a shell, a few options make an allowed binary run something
	// else: `git -c core.pager=…`, `git --upload-pack=…`, and similar. These
	// are properties of the specific tools people put on allowlists, so they
	// are rejected by name rather than by pattern.
	for _, arg := range argv[1:] {
		if arg == "-c" || arg == "--config" || strings.HasPrefix(arg, "--config=") ||
			strings.HasPrefix(arg, "--upload-pack") || strings.HasPrefix(arg, "--receive-pack") ||
			strings.HasPrefix(arg, "--exec") {
			return fmt.Errorf(
				"%w: the %q option can run arbitrary commands and is not allowed for this connector",
				ErrDenied, arg)
		}
	}

	// The daemon inherits the request's env for the child process, so an
	// assignment here has the same reach as an inline `VAR=x cmd` prefix.
	if err := checkExecEnv(payload); err != nil {
		return err
	}

	return nil
}

// safeEnvVars are the environment variables a confined caller may set.
//
// This is an allowlist, not a denylist, because a denylist loses this race
// structurally — the same argument the exec check makes about shell strings.
// Git alone reads arbitrary config from GIT_CONFIG_COUNT/KEY/VALUE (which
// turns an allowlisted `git` into arbitrary code execution with no flag to
// inspect), relocates its view of the filesystem via GIT_DIR and
// GIT_WORK_TREE, and takes hook-shaped commands from GIT_SSH_COMMAND,
// GIT_EXTERNAL_DIFF, GIT_PAGER, GIT_ASKPASS, and GIT_PROXY_COMMAND. Loaders
// add LD_PRELOAD, LD_AUDIT, and the DYLD_* family; language runtimes add
// NODE_OPTIONS, PYTHONPATH, RUBYOPT, PERL5OPT, and JAVA_TOOL_OPTIONS; bash
// adds BASH_ENV and BASH_FUNC_* exported functions. Enumerating that list
// correctly, forever, across every tool a user might allowlist, is not a
// thing anyone can do.
//
// So: a short list of variables that only carry data, and everything else is
// refused with a message naming what was rejected.
var safeEnvVars = map[string]bool{
	"CI":                  true,
	"TERM":                true,
	"NO_COLOR":            true,
	"FORCE_COLOR":         true,
	"LANG":                true,
	"LC_ALL":              true,
	"TZ":                  true,
	"GIT_TERMINAL_PROMPT": true,
	"GIT_COMMITTER_NAME":  true,
	"GIT_COMMITTER_EMAIL": true,
	"GIT_AUTHOR_NAME":     true,
	"GIT_AUTHOR_EMAIL":    true,
}

// checkExecEnv rejects environment variables that are not known to be inert.
func checkExecEnv(payload []byte) error {
	var req struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return fmt.Errorf("%w: request payload was not valid JSON", ErrDenied)
	}
	for key := range req.Env {
		if !safeEnvVars[strings.ToUpper(strings.TrimSpace(key))] {
			return fmt.Errorf(
				"%w: setting %s is not allowed for this connector — an environment variable can redirect "+
					"an allowed command to a different program", ErrDenied, key)
		}
	}
	return nil
}
