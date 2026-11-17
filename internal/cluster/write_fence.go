package cluster

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// THE WRITE FENCE. A command that promises to write nothing — `forge env
// deploy <env> <version> --plan` / `--plan-only`, a spec-change `--dry-run`,
// and the preflight every release deploy runs before it records anything —
// marks its context with WithWritesFenced, and every kubectl invocation forge
// builds through KubectlCommand under that context is refused before it
// starts if it could change cluster state.
//
// Why a fence and not only the dryRun flag. The flag is threaded by hand
// through every stage of the deploy pipeline, and each new stage (a namespace
// pre-create, a kubeconfig mint, a Flux grant) is one more place it can be
// forgotten. A plan that applies is the worst failure a deploy tool can have —
// the reviewer is told nothing will change while prod rolls (forge report
// 88855ea7) — so the promise is enforced at the one place every kubectl
// process is built, where forgetting is not possible: a missed flag now
// surfaces as a loud refusal naming the verb, never as a silent apply.

// ErrWritesFenced is wrapped by the error a fenced kubectl write returns.
var ErrWritesFenced = errors.New("refused a cluster write: this invocation writes nothing")

type writeFenceKey struct{}

// WithWritesFenced returns ctx marked so that every kubectl invocation built
// by KubectlCommand under it refuses a state-changing verb. reason names the
// promise being kept ("--plan-only writes nothing") and is carried into the
// refusal.
func WithWritesFenced(ctx context.Context, reason string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, writeFenceKey{}, reason)
}

// WritesFenced reports whether ctx forbids cluster writes, and why.
func WritesFenced(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	reason, ok := ctx.Value(writeFenceKey{}).(string)
	return reason, ok
}

// KubectlCommand builds `kubectl <args>` (args as passed, `--context` and all)
// honouring the write fence: under WithWritesFenced, a command whose verb can
// change cluster state comes back with Cmd.Err set, so Run, Output and Start
// fail with ErrWritesFenced and no process is started.
//
// It is the one place forge builds a kubectl process (TestKubectlIsBuiltOnlyThroughTheFence
// in internal/cli holds the CLI to it), which is what makes the fence a
// guarantee rather than a convention.
func KubectlCommand(ctx context.Context, args ...string) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	if reason, fenced := WritesFenced(ctx); fenced {
		if verb, writes := kubectlWrites(args); writes {
			cmd.Err = fmt.Errorf("%w (%s): kubectl %s was not run", ErrWritesFenced, reason, verb)
		}
	}
	return cmd
}

// kubectlGlobalFlags is every global flag kubectl accepts before the verb
// (`kubectl options`, v1.36), mapped to whether it takes a value. A
// value-taking flag written without `=` consumes the NEXT argument; a boolean
// one never does. The verb is the first positional argument after them.
//
// The list is complete on purpose, and kubectlVerb fails closed on anything
// missing from it: if an unknown flag that takes a value came before the verb,
// skipping it would read its value as the verb, so `kubectl --unknown get
// apply` would pass as a read. A flag kubectl adds later is refused until it
// is listed here.
var kubectlGlobalFlags = map[string]bool{
	"--as": true, "--as-group": true, "--as-uid": true, "--as-user-extra": true,
	"--cache-dir": true, "--certificate-authority": true, "--client-certificate": true,
	"--client-key": true, "--cluster": true, "--context": true,
	"--kubeconfig": true, "--kuberc": true, "--log-flush-frequency": true,
	"-n": true, "--namespace": true, "--password": true, "--profile": true,
	"--profile-output": true, "--request-timeout": true, "-s": true, "--server": true,
	"--tls-server-name": true, "--token": true, "--user": true, "--username": true,
	"-v": true, "--v": true, "--vmodule": true,
	// Booleans: true only as `--flag` or `--flag=<bool>`.
	"--disable-compression": false, "--insecure-skip-tls-verify": false,
	"--match-server-version": false, "--warnings-as-errors": false,
}

// kubectlReadVerbs never change cluster state.
var kubectlReadVerbs = map[string]bool{
	"get": true, "describe": true, "logs": true, "diff": true, "version": true,
	"api-resources": true, "api-versions": true, "explain": true, "cluster-info": true,
	"top": true, "events": true, "wait": true, "auth": true,
	// config reads and edits the LOCAL kubeconfig, never the cluster.
	"config": true,
}

// kubectlDryRunnableVerbs change state unless they carry a server or client
// dry run.
var kubectlDryRunnableVerbs = map[string]bool{
	"apply": true, "create": true, "patch": true, "replace": true, "delete": true,
}

// kubectlWrites reports the verb of a kubectl argument list and whether it can
// change cluster state. Unknown verbs, and leading flags the parser cannot
// account for, count as writes: the fence fails closed.
func kubectlWrites(args []string) (string, bool) {
	verb, rest, ok := kubectlVerb(args)
	if !ok {
		return strings.Join(args, " "), true
	}
	if verb == "" {
		// No verb at all: kubectl prints its help and changes nothing.
		return "", false
	}
	switch {
	case kubectlReadVerbs[verb]:
		return verb, false
	case verb == "rollout":
		// rollout status / history read; restart, undo, pause, resume write.
		sub, _, subOK := kubectlVerb(rest)
		return "rollout " + sub, !subOK || (sub != "status" && sub != "history")
	case kubectlDryRunnableVerbs[verb]:
		return verb, !kubectlDryRun(rest)
	}
	return verb, true
}

// kubectlVerb returns the first positional argument and what follows it. ok
// is false when a flag before the verb is not a known kubectl global flag, or
// a value-taking flag has no value: the verb cannot then be told apart from a
// flag's value.
func kubectlVerb(args []string) (verb string, rest []string, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return "", nil, false
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return a, args[i+1:], true
		}
		name, _, hasValue := strings.Cut(a, "=")
		takesValue, known := kubectlGlobalFlags[name]
		if !known && !strings.HasPrefix(a, "--") && len(a) > 2 {
			// A short flag with its value attached: -nfoo, -v5.
			if kubectlGlobalFlags[a[:2]] {
				continue
			}
		}
		if !known {
			return "", nil, false
		}
		if takesValue && !hasValue {
			if i+1 >= len(args) {
				return "", nil, false
			}
			i++
		}
	}
	return "", nil, true
}

// kubectlDryRun reports whether the arguments ask for a server or client dry
// run. A bare `--dry-run` is kubectl's deprecated spelling of client.
func kubectlDryRun(args []string) bool {
	for i, a := range args {
		switch a {
		case "--dry-run=server", "--dry-run=client":
			return true
		case "--dry-run":
			return i+1 >= len(args) || args[i+1] != "none"
		}
	}
	return false
}
