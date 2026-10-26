// Copyright (c) 2025 Reliant Labs

// Package credsandbox runs a `credentials: none` command somewhere the daemon's
// credentials are not.
//
// Before this package, `credentials: none` removed credential variables from the
// child's environment and nothing else. The child still ran as the daemon's Unix
// user, so it could read ~/.reliant/daemon.json (the PAT every daemon runs on),
// the forge credentials and token cache, and ~/.git-credentials; it could read
// the daemon's environment and memory through /proc and ptrace; and where the
// container engine's socket is open it could ask the engine for root. A run that
// reads text an outsider can write — an on-call agent reading an error message a
// browser sent — could therefore be told to send those files out (the
// 2026-10-25 security review, H2).
//
// On Linux the command now starts in its own user, mount and PID namespaces,
// set up by this binary re-executed as a small init (HelperArg):
//
//   - the credential files and directories (daemonpolicy.ProtectedCredentials)
//     are absent from the child's view of the filesystem, and so is any second
//     mount of the disk they live on. A file is removed by rebuilding its parent
//     directory without it, not by mounting over the file: the daemon rewrites
//     some of these by rename, and a rename detaches a mount placed on the old
//     file and shows the new contents;
//   - the container engines' sockets are replaced by /dev/null;
//   - the files that decide what a LATER run executes or believes (shell startup
//     files, git config, the global memory file, skills, installed releases) are
//     read-only;
//   - the child sees only its own processes (a fresh /proc), so the daemon's
//     environment and memory are out of reach;
//   - the child has no capabilities and cannot gain any (no_new_privs, empty
//     bounding set); the init that holds the namespace's privileges is not
//     dumpable, so the child cannot attach to it.
//
// What it does not do: the child still shares the network (a git fetch or a
// package install must work), so services bound to loopback stay reachable —
// the daemon therefore must never serve unauthenticated requests on loopback —
// and it can still write the directories it needs to work in. That is the limit
// of "no credentials": a compromised command can still damage the workspace it
// was given.
//
// It fails closed. Where the kernel does not allow unprivileged user, mount and
// PID namespaces (a hardened host, a container whose default seccomp profile
// blocks them), the command is not run, and says why.
//
// Elsewhere (macOS, Windows) there is no equivalent that is both strong and
// unprivileged, so the command keeps only the environment scrub, and the
// daemon's file tools still refuse the Reliant credential files.
//
//forge:exclude-contract: a process-launch helper over os/exec and the kernel; there is no second implementation to substitute
package credsandbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// HelperArg is the first argument of the re-executed binary that sets the
// sandbox up and then runs the command. The program's main (and a test binary's
// TestMain) must call Dispatch before anything else, ahead of pid1.Init: the
// helper is the namespace's init and must not be wrapped by another.
const HelperArg = "__cred-sandbox"

// RefusedExitCode is the exit status of a command the sandbox could not start:
// the shell's "found but could not execute".
const RefusedExitCode = 126

// Plan says what the sandbox takes away from a command and what it freezes.
type Plan struct {
	// Hide are absolute paths made absent: files, sockets and directories.
	Hide []string
	// ReadOnly are absolute paths made read-only.
	ReadOnly []string
}

// readOnlyHomeRel are paths under the user's home that later runs trust: what a
// shell sources, what git is configured with, the memory and skills every chat
// loads, and the releases the daemon is upgraded to. A command that may not
// hold credentials must not be able to plant code or instructions for one that
// may.
var readOnlyHomeRel = []string{
	".bashrc", ".bash_profile", ".bash_login", ".profile", ".bash_logout",
	".zshrc", ".zshenv", ".zprofile",
	".gitconfig", ".config/git",
	".reliant/reliant.md", ".reliant/skills", ".reliant/releases",
}

// DefaultPlan is the plan for this process's user and daemon.
func DefaultPlan() Plan {
	var hide []string
	for _, c := range daemonpolicy.ProtectedCredentials() {
		hide = append(hide, c.Path)
	}
	hide = append(hide, credentialEnvPaths()...)

	var ro []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, rel := range readOnlyHomeRel {
			ro = append(ro, filepath.Join(home, rel))
		}
	}
	return Plan{Hide: cleanAbs(hide), ReadOnly: cleanAbs(ro)}
}

// credentialEnvPaths are the files and sockets the daemon's own credential
// variables point at. The variables are stripped from the child, but what they
// name stays on disk.
func credentialEnvPaths() []string {
	var out []string
	for _, name := range []string{
		"GOOGLE_APPLICATION_CREDENTIALS", "AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "DOCKER_CONFIG", "CLOUDSDK_CONFIG", "GH_CONFIG_DIR",
		"SSH_AUTH_SOCK", "NPM_CONFIG_USERCONFIG",
	} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" { //nolint:forbidigo // names the files the daemon's credential variables point at; not Reliant configuration
			out = append(out, v)
		}
	}
	out = append(out, filepath.SplitList(os.Getenv("KUBECONFIG"))...) //nolint:forbidigo // names the kubeconfig files; not Reliant configuration
	return out
}

func cleanAbs(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		p = filepath.Clean(p)
		if p == "/" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

var (
	disabled   atomic.Bool
	dispatched atomic.Bool
)

// Disable turns the sandbox off for the rest of the process: Apply then leaves
// every command as it was. It is for test binaries whose subject is not the
// sandbox and which run on a host that does not allow it; nothing in the daemon
// calls it.
func Disable() { disabled.Store(true) }

// Enabled reports whether Apply will sandbox a credentials-none command. It is
// true only in a program that called Dispatch: the helper is the program
// itself, re-executed, so a program that does not handle HelperArg must not
// start one (a test binary would run its whole suite again, recursively). The
// reliant binary calls Dispatch first thing in main.
func Enabled() bool { return !disabled.Load() && dispatched.Load() }

// DispatchForTest is Dispatch for a test binary's TestMain whose tests run
// credentials-none commands but are not about the sandbox: on a host that does
// not allow the sandbox it turns it off, so those tests check what they were
// written to check. The sandbox's own tests do not use it.
func DispatchForTest() {
	Dispatch()
	self, err := os.Executable()
	if err != nil || Available(self) != nil {
		Disable()
	}
}

// Wrap applies the sandbox to cmd when credentials is daemonpolicy.CredentialsNone,
// with this process's plan and this binary as the helper. A non-empty refusal
// means the command must not run.
func Wrap(cmd *exec.Cmd, credentials string) (refusal string) {
	if credentials != daemonpolicy.CredentialsNone {
		return ""
	}
	self, err := os.Executable()
	if err != nil {
		return Refusal(fmt.Errorf("locating the sandbox helper binary: %w", err))
	}
	return Apply(cmd, self, DefaultPlan())
}

// ErrUnavailable wraps the reason this host refused the sandbox.
var ErrUnavailable = errors.New("credentials: none needs a filesystem sandbox that this machine did not allow")

// Refusal is what a command that could not start in the sandbox reports.
func Refusal(cause error) string {
	return fmt.Sprintf("reliant: `credentials: none` command not run: %v. "+
		"Without the sandbox it could read the daemon's credential files, so it was refused. "+
		"Run the daemon where unprivileged user, mount and PID namespaces are permitted "+
		"(inside a container, its default seccomp profile blocks them).", cause)
}
