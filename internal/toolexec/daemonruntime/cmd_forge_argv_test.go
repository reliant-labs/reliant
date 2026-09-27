// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"strings"
	"testing"

	forgecli "github.com/reliant-labs/forge/cli"
)

// The rest of this package's forge tests pin the argv each handler builds
// against a literal. A literal only proves the handler is self-consistent; it
// cannot notice forge changing underneath it. That is exactly how
// `forge secret list <env> --json` survived forge moving the env to a required
// --env flag: every test stayed green while the rebuilt daemon's real
// invocation would have been rejected.
//
// This test closes the gap by parsing every argv the daemon hands forge
// against the forge command tree this binary EMBEDS (forgecli.NewRootCmd is
// what `reliant forge …` runs). It resolves the subcommand, parses flags,
// validates positional args and required flags — everything cobra checks
// before RunE — without running anything. A forge CLI change that breaks a
// daemon caller now fails here, in `go test`, rather than in a user's daemon.

// forgeArgvUnderTest collects the argv every forge-invoking daemon path builds,
// captured from the real handlers/builders rather than restated.
func forgeArgvUnderTest(t *testing.T) map[string][]string {
	t.Helper()
	argv := map[string][]string{}

	for _, tc := range []struct {
		name    string
		command string
		payload map[string]any
	}{
		{"topology", "forge.topology", nil},
		{"topology verify envs", "forge.topology", map[string]any{"verify": true, "envs": []string{"dev", "prod"}}},
		{"env_verify", "forge.env_verify", map[string]any{"env": "prod"}},
		{"secret_list", "forge.secret_list", map[string]any{"env": "dev"}},
		{"audit", "forge.audit", nil},
		{"env_status", "forge.env_status", map[string]any{"env": "dev"}},
	} {
		dir := forgeProject(t)
		call := stubForge(t, forgeCommandResult{Stdout: []byte(`{"ok":true}`)}, nil)
		payload := map[string]any{"project_path": dir}
		for k, v := range tc.payload {
			payload[k] = v
		}
		if _, err := handle(t, tc.command, payload); err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.name, err)
		}
		if call.Count != 1 {
			t.Fatalf("%s: expected one forge invocation, got %d", tc.name, call.Count)
		}
		argv[tc.name] = call.Args
	}

	deploy := forgeDeployArgs{ProjectPath: "/p", Env: "prod"}
	argv["deploy plan"] = deploy.planArgs()
	argv["deploy apply"] = deploy.applyArgs()

	promote := forgePromoteArgs{ProjectPath: "/p", Env: "prod", Release: "v1.2.3"}
	argv["promote plan"] = promote.planArgs()
	argv["promote apply"] = promote.applyArgs()

	return argv
}

// parseAgainstEmbeddedForge runs cobra's pre-RunE checks for args on a fresh
// forge root, prefixed exactly as runForgeSelfExec prefixes them.
func parseAgainstEmbeddedForge(args []string) (string, error) {
	root := forgecli.NewRootCmd()
	full := append([]string{"--silence-experimental"}, args...)
	cmd, rest, err := root.Find(full)
	if err != nil {
		return "", err
	}
	if cmd == root || !cmd.Runnable() {
		return cmd.CommandPath(), errNotALeafCommand(cmd.CommandPath())
	}
	if err := cmd.ParseFlags(rest); err != nil {
		return cmd.CommandPath(), err
	}
	if err := cmd.ValidateArgs(cmd.Flags().Args()); err != nil {
		return cmd.CommandPath(), err
	}
	if err := cmd.ValidateRequiredFlags(); err != nil {
		return cmd.CommandPath(), err
	}
	return cmd.CommandPath(), nil
}

type errNotALeafCommand string

func (e errNotALeafCommand) Error() string {
	return "argv resolves to non-runnable command " + string(e)
}

func TestForgeDaemonArgvParsesAgainstEmbeddedForge(t *testing.T) {
	for name, args := range forgeArgvUnderTest(t) {
		t.Run(name, func(t *testing.T) {
			path, err := parseAgainstEmbeddedForge(args)
			if err != nil {
				t.Fatalf("embedded forge rejects daemon argv %q (resolved %q): %v", args, path, err)
			}
			// Every daemon call is a two-word forge subcommand; resolving to
			// anything else means a leading arg was swallowed as a command.
			if want := "forge " + strings.Join(args[:2], " "); path != want {
				t.Errorf("argv %q resolved to %q, want %q", args, path, want)
			}
		})
	}
}

// TestEmbeddedForgeRejectsPositionalSecretEnv is the guard's own proof: the
// pre-fix argv must FAIL the same parse, or the test above could never have
// caught the regression it exists for.
func TestEmbeddedForgeRejectsPositionalSecretEnv(t *testing.T) {
	if _, err := parseAgainstEmbeddedForge([]string{"secret", "list", "dev", "--json"}); err == nil {
		t.Fatal("embedded forge accepted `secret list dev --json`; the argv guard cannot detect a positional-env regression")
	}
}

func TestForgeSecretListPinsEnvFlag(t *testing.T) {
	// Pinned exactly: an env that begins with "-" must stay bound to --env
	// rather than being parsed as a flag of its own.
	dir := forgeProject(t)
	call := stubForge(t, forgeCommandResult{Stdout: []byte(`{"ok":true}`)}, nil)
	if _, err := handle(t, "forge.secret_list", forgeSecretListRequest{ProjectPath: dir, Env: "--json"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"secret", "list", "--env=--json", "--json"}
	if strings.Join(call.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args:\n got %q\nwant %q", call.Args, want)
	}
}
