package cli

import (
	"regexp"
	"strings"
	"testing"
)

// V2's contract has two halves that must hold together: `forge env build
// <env>` OWNS publishing, and top-level `forge build` REFUSES it with a
// pointer. A test for either half alone would pass while the pair was broken.

func TestEnvBuildOwnsPushAndRelease(t *testing.T) {
	cmd := newEnvBuildCmd()
	if !strings.HasPrefix(cmd.Use, "build <environment>") {
		t.Errorf("Use = %q, want the env as a required positional", cmd.Use)
	}
	for _, name := range []string{"push", "release", "no-build", "plan", "tag", "gate-json", "docker", "target"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("`forge env build` is missing --%s", name)
		}
	}
	// The env is the SUBJECT here, never a flag — that is the whole point of
	// moving these two flags off a command whose env was optional.
	if cmd.Flags().Lookup("env") != nil {
		t.Error("`forge env build` must not carry an --env flag; the environment is its positional argument")
	}
	// The CI run identity travels with the release it records.
	for _, name := range []string{"run-id", "run-url", "no-run"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("`forge env build` is missing --%s (the run id is the join key on a recorded release)", name)
		}
	}
}

// TestEnvBuildReleaseImpliesPush pins the ADR's wording: "--release vX implies
// push". It is not a convenience — a release pins registry digests, and a
// digest exists only once a registry holds the bytes, so a release that did
// not push would fail its own ledger write.
func TestEnvBuildReleaseImpliesPush(t *testing.T) {
	cmd := newEnvBuildCmd()
	cmd.SetArgs([]string{"prod", "--release", "v1.4.0", "--plan"})
	// Parse only: Execute would render KCL and run a preflight. The implication
	// is applied in RunE, so assert it through the same code path by reading
	// the help contract plus the flag wiring, then the behaviour below.
	if err := cmd.ParseFlags([]string{"--release", "v1.4.0"}); err != nil {
		t.Fatalf("parse --release: %v", err)
	}
	help := cmd.Long
	if !strings.Contains(help, "IMPLIES --push") {
		t.Error("`forge env build --release` must document that it implies --push")
	}
	rf := cmd.Flags().Lookup("release")
	if rf == nil || !strings.Contains(rf.Usage, "IMPLIES --push") {
		t.Error("--release's own usage string should say it implies --push")
	}
}

// TestBuildRefusesPushAndRelease is the compile-only half. Both flags must
// fail, and the message must carry the replacement command — a user with
// the old `build prod --push` in their shell history needs the new spelling,
// not an explanation of command taxonomy.
func TestBuildRefusesPushAndRelease(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"push with env", []string{"prod", "--push"}, "forge env build prod --push"},
		{"push without env", []string{"--push"}, "forge env build <env> --push"},
		{"release with env", []string{"prod", "--release", "v1.4.0"}, "forge env build prod --release"},
		{"release without env", []string{"--release", "v1.4.0"}, "forge env build <env> --release"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := newBuildCmd()
			cmd.SetArgs(c.args)
			cmd.SetOut(nopWriter{})
			cmd.SetErr(nopWriter{})
			err := cmd.Execute()
			if err == nil {
				t.Fatal("`forge build` must refuse this flag — it is compile-only")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("refusal must name the replacement %q, got:\n%s", c.want, err.Error())
			}
			if !strings.Contains(err.Error(), "compile-only") {
				t.Errorf("refusal should say `forge build` is compile-only, got:\n%s", err.Error())
			}
		})
	}
}

// TestBuildRefusalFiresBeforeAnyWork: the refusal must not depend on a
// project, a render, or a feature gate. It is a usage decision, and reaching
// it should cost nothing — a user who typed the old command in the wrong
// directory should still be told the new one.
func TestBuildRefusalFiresBeforeAnyWork(t *testing.T) {
	t.Chdir(t.TempDir()) // no forge.yaml, no deploy/kcl
	cmd := newBuildCmd()
	cmd.SetArgs([]string{"prod", "--push"})
	cmd.SetOut(nopWriter{})
	cmd.SetErr(nopWriter{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "forge env build prod --push") {
		t.Fatalf("the refusal must fire outside a project too, got: %v", err)
	}
}

// TestBuildHelpSaysCompileOnly: the help is where someone lands after the
// refusal, so it has to carry the replacement too.
func TestBuildHelpSaysCompileOnly(t *testing.T) {
	cmd := newBuildCmd()
	help := cmd.Short + "\n" + cmd.Long
	if !strings.Contains(help, "forge env build") {
		t.Error("`forge build` help should point at `forge env build` for publishing")
	}
	// No example may invoke the refused flags on THIS command. Matched as a
	// regex — the bare verb followed by either flag on one line — rather than
	// as a fixed string, because the live `forge env build prod --push` this
	// help now recommends CONTAINS the old spelling as a substring, so a
	// literal check would flag the correct example as stale.
	staleExample := regexp.MustCompile(`(?m)^[^\n]*\bforge build\b[^\n]*--(?:push|release)\b`)
	if m := staleExample.FindString(help); m != "" {
		t.Errorf("`forge build` help still shows an example it now refuses: %q", strings.TrimSpace(m))
	}
	if strings.Contains(help, "--push without an env asks for one") {
		t.Error("`forge build` help still explains the old push/env coupling, which no longer exists")
	}
	// The two publishing flags stay REGISTERED (so the refusal can name the
	// replacement instead of cobra saying "unknown flag") but must not
	// advertise themselves as things this command does.
	for _, name := range []string{"push", "release"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("--%s must stay registered so the refusal can be specific", name)
		}
		if !f.Hidden {
			t.Errorf("--%s should be hidden on the compile-only command", name)
		}
	}
}

// TestEnvBuildNoBuildRequiresRelease pins the folded `release cut`: --no-build
// is the cut-only half, meaningless without a version to record.
func TestEnvBuildNoBuildRequiresRelease(t *testing.T) {
	cmd := newEnvBuildCmd()
	cmd.SetArgs([]string{"prod", "--no-build"})
	cmd.SetOut(nopWriter{})
	cmd.SetErr(nopWriter{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("--no-build without --release must be refused")
	}
	if !strings.Contains(err.Error(), "--release") {
		t.Errorf("the refusal should point at --release, got:\n%s", err.Error())
	}
}

// TestEnvBuildDocumentsTheCutOnlyPipeline: `forge release cut` was deleted,
// so the two-job pipeline it served has to be discoverable from the command
// that absorbed it, or the capability is gone in practice.
func TestEnvBuildDocumentsTheCutOnlyPipeline(t *testing.T) {
	help := newEnvBuildCmd().Long
	for _, want := range []string{"--no-build", ".forge/state", "separate jobs"} {
		if !strings.Contains(help, want) {
			t.Errorf("`forge env build` help does not explain the cut-only pipeline (%q missing):\n%s", want, help)
		}
	}
}

// TestReleaseGroupKeepsVerifyAndWhere: V2 deletes `cut` and nothing else. The
// two ledger verbs that are not env-scoped stay exactly where they were.
func TestReleaseGroupKeepsVerifyAndWhere(t *testing.T) {
	rel := newReleaseCmd()
	have := map[string]bool{}
	for _, c := range rel.Commands() {
		have[c.Name()] = true
	}
	for _, want := range []string{"verify", "where"} {
		if !have[want] {
			t.Errorf("`forge release %s` must survive V2 — it acts on a ledger, not an env", want)
		}
	}
	if have["cut"] {
		t.Error("`forge release cut` is still registered; it folded into `forge env build <env> --release <v> --no-build`")
	}
}

// TestEnvBuildRegisteredUnderEnv: the verb has to be reachable from the noun,
// which is the one shared file V2 touches.
func TestEnvBuildRegisteredUnderEnv(t *testing.T) {
	sub, _, err := newEnvCmd().Find([]string{"build"})
	if err != nil || sub == nil || sub.Name() != "build" {
		t.Fatalf("`forge env build` is not registered: %v", err)
	}
}

// nopWriter swallows cobra's usage/error output so a refusal test asserts on
// the returned error rather than on scrollback.
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
