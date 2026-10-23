// Tests for the user-vs-maintainer CLI surface split as it applies to
// `forge lint`. The visible flag set is pinned here on purpose: a new lint
// flag must consciously pick a side (visible user surface vs hidden
// --help-dev surface) or these tests fail. The generate half of the split
// stays in internal/cli/help_surface_test.go.

package lint

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// visibleFlagNames returns the sorted names of all non-hidden local flags
// on cmd — exactly what cobra renders under "Flags:" in --help.
func visibleFlagNames(cmd *cobra.Command) []string {
	var names []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !f.Hidden {
			names = append(names, f.Name)
		}
	})
	sort.Strings(names)
	return names
}

// hiddenFlagNames returns the sorted names of all hidden local flags.
func hiddenFlagNames(cmd *cobra.Command) []string {
	var names []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			names = append(names, f.Name)
		}
	})
	sort.Strings(names)
	return names
}

func assertStringSlicesEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s mismatch:\n  got:  %v\n  want: %v\n(new flags must consciously pick a side — see cmdutil.HideDevFlags)", what, got, want)
	}
}

func TestLintHelpSurface(t *testing.T) {
	cmd := newCmd(testFactory())

	assertStringSlicesEqual(t, "lint visible flags", visibleFlagNames(cmd), []string{
		// User surface: a gating lane that could NOT run fails the run, and
		// this is the one named, reasoned way to accept it may not. Hidden,
		// the only way past a missing toolchain would be to drop the gate.
		"allow-skip",
		// Report-only: the dry run of the auto-fix default, and what CI gets
		// without asking. Visible because the CI behaviour is otherwise a
		// surprise an author only meets on a red build.
		"check",
		// A continuous rule about the user's own schema, and the only
		// hermetic place a CHECK that mirrors a Go set (forge's access-token
		// scopes) is compared with it: the project tests that did this
		// needed a database and skipped on the -short tier.
		"closed-sets",
		// Continuous rules about the USER's own code. config-deps,
		// frontend-stores and optional-deps-guard were all hidden as
		// "audits" and were consequently undiscoverable: on the flagship app
		// config-deps alone reported 15 real findings with paste-ready
		// remediation, while a freshly scaffolded project comes up clean on
		// all three — advisory, but not noisy.
		"column-markers",
		// A continuous rule about the user's own protos + handlers: a
		// forge:computed field nothing populates takes the column default,
		// which for the money columns this marker mostly lands on ships as
		// $0.00 with no constraint violated, no test failing, and no log
		// line. Visible because a human reading a screen is otherwise the
		// only detector.
		"computed-fields",
		"config-deps",
		// A continuous rule about the user's own config protos: with
		// per-binary configs a whole config message can be bound to a
		// binary that does not exist, and nothing else in forge reports
		// it — the fields simply generate and are never loaded.
		"config-reach",
		"contract",
		"conventions",
		// A continuous rule about the user's own project, and the only
		// place this failure is legible at all: a foreign key added after
		// handlers_crud_test.go was scaffolded strands the seed literals
		// in a file forge deliberately never regenerates, and the only
		// other signal is a pq constraint error in test setup — one per
		// affected package, reading like a broken harness.
		// A continuous rule about the user's own protos, and the only place
		// this failure is legible: Create<Entity>Request is the one envelope
		// that FLATTENS the entity, so it re-declares each field's
		// `optional` label, and a label lost there collapses absent and
		// empty. The write lands as "" in a nullable column and surfaces as
		// a postgres foreign-key violation naming a constraint, never the
		// proto line that caused it.
		"create-nullability",
		// Visible: a continuous rule about the user's own schema, and the only
		// place a credential column the read-only role is refused surfaces by
		// name. Left hidden, a project would learn forge_ro cannot read a
		// column from a query failing in production.
		"db-grants",
		"fix",
		// Visible because it is the only place a scaffold-once seed block's
		// drift is legible: a fixture a later migration rejects fails in
		// test setup, with an error naming neither the fixture nor the
		// migration, in a file forge never rewrites. (--crud-fixtures is
		// its deprecated, hidden alias.)
		"fixture-drift",
		"frontend-stores",
		// Visible: a CI author has to be able to DISCOVER that lint's
		// result can be recorded as release evidence. A hidden
		// --gate-json would mean the evidence trail stays empty for
		// every pipeline whose author never read the design doc, which
		// is the adoption failure the flag exists to avoid.
		"gate-json",
		"generated-drift",
		// The Go-source twin of proto-markers, and the only place a
		// `//forge:` comment forge ignores surfaces: reliant's
		// `//forge:exclude-contract — why` left a package un-excluded while
		// the contract lint failed with no hint. Visible for the same reason.
		"go-markers",
		// A continuous rule about the user's own composition: a service no
		// root reaches is a service no process serves, and a listener over
		// the wrong audience's chain lets one trust domain reach another.
		"graph",
		// Visible because it is the ONLY signal for a defect regenerating
		// cannot reach: scaffolded pages are write-if-absent, so an edit
		// page that predates a `forge:guards` marker keeps writing the
		// guarded column forever. It typechecks, nothing logs, and the
		// symptom is a user's save returning a 500 from the raw CHECK.
		"guarded-fields",
		"help-dev",
		"json",
		"migration-safety",
		"no-fix",
		"optional-deps-guard",
		// A continuous rule about the user's own protos, and the only place
		// a misspelled forge:* marker surfaces at all — visible for the same
		// reason column-markers is.
		"proto-markers",
		// The OPTION-field twin of proto-markers, and the rule that would
		// have caught 104 inert authz annotations in the flagship app.
		// Visible for the same reason: it is a continuous rule about the
		// user's own protos, and the only place the failure surfaces.
		"proto-options",
		// Visible because the people who need it most are agents that gate
		// phases on lint and keep their context small: they piped full
		// output through `head` and lost the verdict at the bottom.
		"quiet",
		// The computed-fields twin for the marker where the failure is
		// SILENT rather than merely unreported. forge:read-only strips a
		// field from every write envelope and promises nothing in return,
		// so a plain zero-default money column is written by nothing at
		// all — and unlike the computed case the author declared no
		// obligation anyone could check. Visible for the same reason:
		// a human reading $0.00 on a screen is otherwise the only detector.
		"read-only-fields",
		// User surface: an agent sharing a checkout with others needs to
		// lint ITS slice without another agent's half-written file
		// failing the run.
		"scope",
		// Visible: the continuous rule about the user's own secrets
		// declarations. A Secret a pod reads that nothing produces, a key
		// ESO drops, and an ExternalSecret that deletes its Secret on a prune
		// all surface only at deploy or later.
		"secrets",
		// Visible: the audit every suppression mechanism's docs promise
		// (contracts/package-markers names it). Each suppression is
		// visible at its own site and nowhere together, which is how a
		// codebase ends up disabling everything one line at a time.
		"show-suppressed",
		// User surface, not maintainer: it is the answer to "how do I lint
		// the backend without paying for the Node toolchain", and it mirrors
		// the frontend-skipping vocabulary `forge build` already uses.
		"skip-frontends",
		// A continuous rule about the user's own tests: one that execs a
		// toolchain, drives containers or dials an external host without
		// being marked slow sits in the blocking -short tier, and a file its
		// child process reads is invisible to Go's test cache. Opt-in until
		// forge's and reliant's suites are migrated (both still report
		// findings, mostly git fixture repos).
		"slow-tests",
		// Visible: a static runtime (hosted, bucket, firebase) serves files
		// only, and `next dev` serves everything a static export cannot —
		// so without this the first signal is the export failing in CI, or
		// production quietly lacking what dev had.
		"static-export",
		"strict",
		"tests",
		// The read-only-fields twin for REPORTING queries, and silent in
		// the same way: a two-argument date_trunc over a TIMESTAMPTZ
		// buckets in the session's timezone, which the driver takes from
		// the client host, so every total is attributed to the wrong day
		// by an amount that changes with the deploy host. Nothing fails
		// and the chart still renders, so the only other detector is a
		// human noticing that CI and a laptop disagree. Visible also
		// because forge itself routes reporting screens to raw SQL — the
		// generated ORM cannot express GROUP BY — so this is a lane
		// forge's own guidance leads authors into.
		"time-bucketing",
		// Gating, and the migration command for the log rule: a repo that
		// dialled the tracing rules to warn adopts them by running this lane,
		// which rewrites every log call. Hidden, nobody would find it.
		"tracing",
		// Gating, and about the user's project: forge COPIES these protos
		// in and then never tracks them, so a stale vendored forge.proto
		// is invisible to every other command. Hiding the one check that
		// sees it would recreate the silence.
		"vendored-protos",
	})

	// What stays hidden: meaningless outside the forge repo, bundled into
	// a visible flag, or a one-shot you run at setup and never again.
	assertStringSlicesEqual(t, "lint hidden flags", hiddenFlagNames(cmd), []string{
		"banners",
		"check-workarounds",
		// Deprecated alias of --fixture-drift, which now executes the
		// fixtures and so reports the foreign-key class this flag checked.
		"crud-fixtures",
		"exported-vars",
		"scaffolds",
		"suggest-buf-excepts",
		"suggest-excludes",
	})

	// Hidden flags must not leak into the rendered help.
	usage := cmd.UsageString()
	for _, name := range hiddenFlagNames(cmd) {
		if strings.Contains(usage, "--"+name) {
			t.Errorf("hidden lint flag --%s leaked into --help usage output", name)
		}
	}
	// The Long text must point at the discoverability mechanism.
	if !strings.Contains(cmd.Long, "--help-dev") {
		t.Error("lint Long help must mention --help-dev so hidden flags stay discoverable")
	}
}

// TestLintHiddenFlagsStillParse proves hiding is help-only: hidden lint
// flags must still parse and set their values exactly as before.
func TestLintHiddenFlagsStillParse(t *testing.T) {
	cmd := newCmd(testFactory())
	args := []string{"--banners", "--check-workarounds", "--config-deps"}
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("Parse(%v) error = %v (hidden flags must remain functional)", args, err)
	}
	for name, want := range map[string]string{"banners": "true", "check-workarounds": "true", "config-deps": "true"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("flag --%s not registered", name)
		}
		if got := f.Value.String(); got != want {
			t.Errorf("--%s parsed value = %q, want %q", name, got, want)
		}
	}
}

// TestLintHelpDevListsHiddenFlags runs `forge lint --help-dev` end-to-end
// and asserts it exits cleanly, lists every hidden flag, and does NOT run
// the underlying command.
func TestLintHelpDevListsHiddenFlags(t *testing.T) {
	cmd := newCmd(testFactory())
	hidden := hiddenFlagNames(cmd)

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help-dev"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("forge lint --help-dev: unexpected error: %v", err)
	}
	for _, name := range hidden {
		if !strings.Contains(out.String(), "--"+name) {
			t.Errorf("forge lint --help-dev output missing hidden flag --%s\noutput:\n%s", name, out.String())
		}
	}
}
