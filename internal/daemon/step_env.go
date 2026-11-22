// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"sort"
	"strings"
)

// CapabilityRunStepEnv is what a daemon that applies RunCommandRequest.StepEnv
// lists in its registration. The server sends a step key (a run node's
// llm_gateway) only to a machine whose registration lists it: an older daemon
// would ignore the field and run the command without the key, or with the
// machine's own provider sign-in instead.
const CapabilityRunStepEnv = "run_step.llm_gateway_env"

// StepEnv is the environment a workflow run step's command gets on top of
// everything else (RunCommandRequest.StepEnv): applied after the daemon's own
// environment policy (daemonpolicy.ChildEnv, so `credentials: none` cannot
// strip it) and after the request's Env (so the step's own `env:` cannot
// redirect it).
//
// Only the server builds one, for a run step (the context map's
// run_step_env, never tool input). Today it carries an LLM gateway step key.
type StepEnv struct {
	// Set are plain variables the command gets: a gateway URL, model names.
	Set map[string]string `json:"set,omitempty"`
	// Secret are variables whose values are redacted from the command's
	// output wherever the daemon or the worker report it.
	Secret SecretEnv `json:"secret,omitempty"`
	// Unset are names removed from the command's environment, inherited or
	// requested, so a variable that would override the step's (a provider API
	// key, a cloud-provider switch) never reaches it.
	Unset []string `json:"unset,omitempty"`
}

// SecretEnv is a StepEnv's secret variables. It prints as its names only, in
// every format and in slog, so a log line or an error that carries a request
// never carries a value. JSON carries the values: it is the wire.
type SecretEnv map[string]string

// String is the names, never the values.
func (s SecretEnv) String() string {
	if len(s) == 0 {
		return "[]"
	}
	return "[redacted: " + strings.Join(s.names(), ", ") + "]"
}

// GoString is String: %#v must not print the values either.
func (s SecretEnv) GoString() string { return s.String() }

// Format makes every verb print String.
func (s SecretEnv) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.String()) }

// LogValue is String.
func (s SecretEnv) LogValue() slog.Value { return slog.StringValue(s.String()) }

func (s SecretEnv) names() []string {
	names := make([]string, 0, len(s))
	for name := range s {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Empty reports whether e changes nothing.
func (e *StepEnv) Empty() bool {
	return e == nil || (len(e.Set) == 0 && len(e.Secret) == 0 && len(e.Unset) == 0)
}

// HasSecret reports whether e carries a secret value.
func (e *StepEnv) HasSecret() bool {
	return e != nil && len(e.Secret) > 0
}

// Apply is env (KEY=VALUE entries) with every name e unsets, sets or holds a
// secret for removed, then e's variables appended. A name the command must
// not see is gone even when it was inherited, and none of e's names keeps an
// earlier value.
func (e *StepEnv) Apply(env []string) []string {
	if e.Empty() {
		return env
	}
	drop := make([]string, 0, len(e.Unset)+len(e.Set)+len(e.Secret))
	drop = append(drop, e.Unset...)
	for name := range e.Set {
		drop = append(drop, name)
	}
	for name := range e.Secret {
		drop = append(drop, name)
	}
	out := make([]string, 0, len(env)+len(e.Set)+len(e.Secret))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !containsEnvName(drop, name) {
			out = append(out, kv)
		}
	}
	for _, name := range sortedKeys(e.Set) {
		out = append(out, name+"="+e.Set[name])
	}
	for _, name := range e.Secret.names() {
		out = append(out, name+"="+e.Secret[name])
	}
	return out
}

// Redact replaces every secret value in s with "[redacted: NAME]".
func (e *StepEnv) Redact(s string) string {
	if !e.HasSecret() || s == "" {
		return s
	}
	return RedactSecrets(s, e.Secret)
}

// RedactSecrets replaces each non-empty value of secrets in s with
// "[redacted: NAME]", longest value first so one that contains another is
// replaced whole.
func RedactSecrets(s string, secrets map[string]string) string {
	if s == "" || len(secrets) == 0 {
		return s
	}
	names := make([]string, 0, len(secrets))
	for name, value := range secrets {
		if value != "" {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if len(secrets[names[i]]) != len(secrets[names[j]]) {
			return len(secrets[names[i]]) > len(secrets[names[j]])
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		s = strings.ReplaceAll(s, secrets[name], "[redacted: "+name+"]")
	}
	return s
}

// containsEnvName reports whether names holds name, case-insensitively where
// the platform's environment is (Windows).
func containsEnvName(names []string, name string) bool {
	for _, n := range names {
		if n == name || (runtime.GOOS == "windows" && strings.EqualFold(n, name)) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// noBackgroundProbe is installed in place of a BackgroundProbe for a command
// that must not be detached: PollForBackgroundDetach finds no probe.
type noBackgroundProbe struct{}

// withoutBackgroundDetach is ctx with no background probe: a command holding a
// step key is never detached into a background process, whose output is read
// later (shell_output) without the redaction its result gets.
func withoutBackgroundDetach(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundRequestKey, noBackgroundProbe{})
}
