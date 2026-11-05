package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/module"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/forgecompat"
)

// forge↔project compatibility check (kalshi fr-ac69216583). The decision
// itself lives in internal/forgecompat (shared with `forge env render`'s
// stale-vendor advice); this file owns generate's refusal prose.
//
// The refusals below deliberately say nothing about the state of the tree.
// Whether generate changed anything is a fact about the pipeline run, not
// about this check, so the pipeline reports it from its write journal (see
// reportUnchangedTree in generate.go). Prose here that asserted "No files
// were changed" was false the moment any step ran before this one — which
// is exactly how a refused generate reported an unchanged tree over a
// freshly rewritten .forge-kcl/.

// forgeModuleRequirePath is the module a project requires to get forge's
// runtime libraries.
const forgeModuleRequirePath = forgecompat.ModulePath

// legacyForgePkgRequireRE matches a direct require on the retired
// github.com/reliant-labs/forge/pkg module.
var legacyForgePkgRequireRE = regexp.MustCompile(
	`(?m)^[\t ]*(?:require[\t ]+)?github\.com/reliant-labs/forge/pkg[\t ]+(v[^\s]+)[\t ]*$`)

// checkPkgCompat verifies, BEFORE codegen mutates the tree, that the forge
// this project compiles against can satisfy the code this binary generates.
//
// Returns a user-facing error on a genuine mismatch, and nil
// whenever the question doesn't apply or can't be answered — no go.mod, no
// forge requirement, or a toolchain that won't tell us how forge resolved.
// generate's existing validate step remains the backstop for those.
func checkPkgCompat(projectDir string) error {
	// The pin-vs-running-binary mismatch is said ONCE per generate, by
	// stepAnnounceProject. It used to be said here too, in the same run
	// and about the same condition, so every generate against a pinned
	// project printed two warnings that gave the same advice in
	// different words. Two spellings of one fact read as two problems.
	data, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return nil // no module → nothing to check; not our error to raise
	}
	gomod := string(data)

	// A DIRECT require on the retired submodule is fatal and produces the
	// least legible error in Go: both github.com/reliant-labs/forge (which
	// now contains pkg/) and github.com/reliant-labs/forge/pkg provide the
	// import path github.com/reliant-labs/forge/pkg/<x>, so requiring both
	// answers every such import with
	//
	//	ambiguous import: found package github.com/reliant-labs/forge/pkg/orm
	//	in multiple modules
	//
	// repeated once per import, naming no cause and no fix. Reading this
	// project's OWN go.mod is in scope; going looking for the same module
	// elsewhere in the dependency graph is not — see
	// directRetiredPkgRequire.
	//
	// gen/go.mod is not read: generate repairs it itself (see
	// ensureGenForgeRequire), which is how the retired require used to come
	// back via a plain `go mod tidy` there.
	if retired := projectRetiredPkgRequires(projectDir, gomod); len(retired) > 0 {
		return retiredPkgModuleErr(projectDir, retired)
	}
	assessment, ok := forgecompat.Assess(projectDir)
	if !ok {
		return nil // no forge requirement, or the toolchain can't answer → validate backstop
	}
	switch assessment.Verdict {
	case forgecompat.UnreleasableNoBridge:
		return unreleasableBuildErr(projectDir, assessment.ProjectVersion)
	case forgecompat.StalePin:
		return staleForgePinErr(projectDir, assessment.ProjectVersion, assessment.BinaryVersion)
	case forgecompat.BehindPin:
		return binaryBehindPinErr(projectDir, assessment.ProjectVersion, assessment.BinaryVersion)
	}
	return nil
}

// binaryBehindPinErr is the refusal for a binary OLDER than the project's
// pin. It is a refusal rather than a warning because forge's KCL schema
// module is embedded in the binary: generate renders the project's
// deploy/kcl, so an older binary evaluates it against an older schema and
// reports a field the project is entitled to use as an error on the
// PROJECT's line number. See internal/forgecompat/skew.go.
func binaryBehindPinErr(projectDir, projectVersion, binaryVersion string) error {
	base := cliutil.UserErr("forge generate (forge version compatibility)",
		forgecompat.SkewDiagnosis(projectVersion, binaryVersion),
		"",
		fmt.Sprintf("run this command with the pinned forge, installing nothing over the one on your PATH:\n"+
			"    %s\n"+
			"  or install the pinned forge (the command above), or if the PIN is what is "+
			"wrong, move it deliberately with `%s project upgrade`. To proceed anyway: %s=1",
			pinnedRunLine(projectDir, projectVersion, forgeInvocationArgs()),
			Name(), forgecompat.SkewOverrideEnv))

	return fmt.Errorf("%w\n\n%s", base, toolchainDiagnosis(projectDir))
}

// directRetiredPkgRequire returns the version of the retired forge/pkg module
// if THIS PROJECT requires it directly, reading only its go.mod.
//
// IT DELIBERATELY DOES NOT LOOK AT THE DEPENDENCY GRAPH. It used to: it ran
// `go list -m github.com/reliant-labs/forge/pkg`, which resolves the whole
// build list, and refused to generate when the retired module appeared
// anywhere — then told the user to run `go mod why` and go fix a dependency.
//
// That is not forge's call to make. control-plane imports reliant, which
// imports forge, so that probe failed control-plane's generate because
// RELIANT had not bumped, while control-plane's own code and pins were
// correct. For anyone outside this org it is worse: "a dependency of yours
// requires forge/pkg, go fix that dependency" is advice they cannot act on,
// from a tool claiming authority over a graph it does not own — to cover a
// migration window that ages out.
//
// go.mod resolution is sufficient here, and MVS is why: Go selects ONE
// maximum version of forge for the whole build, so the library side is
// coherent no matter what a project and somebody else's library each pin. A
// real mismatch between the generating binary and that selected library is an
// ordinary compile error, and forge already answers that EMPIRICALLY by
// emitting, validating, and rolling back — which beats predicting it from
// version strings.
//
// A DIRECT require stays in scope: it is the project's own declaration, it
// makes every forge/pkg/* import ambiguous so nothing can build, and
// `go mod edit -droprequire` is something the reader can actually run.
func directRetiredPkgRequire(gomod string) (version string, found bool) {
	if m := legacyForgePkgRequireRE.FindStringSubmatch(gomod); m != nil {
		return m[1], true
	}
	return "", false
}

// unreleasableBuildErr is the error for the case that used to produce
// `undefined: testkit.StubNotConfigured` deep in validate: an unreleased
// forge generating into a project pinned to a published one.
func unreleasableBuildErr(projectDir, projectVersion string) error {
	resolved, _, _ := buildinfo.ResolveForgeRoot("")
	root := resolved.Dir
	bridge := "go work init . && go work use . <path-to-your-forge-checkout>"
	if root != "" {
		bridge = fmt.Sprintf("go work init . && go work use . %s", root)
	}

	base := cliutil.UserErr("forge generate (forge version compatibility)",
		fmt.Sprintf("this forge is %s — an unreleased build no module proxy can serve — but the project "+
			"resolves %s to the published %s. The generated code would call into a forge this project "+
			"cannot fetch, so generating would rewrite the tree and then fail its own validate",
			buildinfo.Version(), forgeModuleRequirePath, projectVersion),
		"",
		"run this command with the forge the project pins — nothing is installed over the forge on your PATH:\n"+
			"    "+pinnedRunLine(projectDir, projectVersion, forgeInvocationArgs())+
			"\n  Or, to generate with THIS unreleased forge, bridge the project to its source:\n    "+bridge+
			"\n  go.work is machine-local; keep it out of version control")

	return fmt.Errorf("%w\n\n%s", base, toolchainDiagnosis(projectDir))
}

// staleForgePinErr is the ordinary skew: a released forge newer than the
// project's pin.
func staleForgePinErr(projectDir, projectVersion, binaryVersion string) error {
	fix := fmt.Sprintf("if the pin is genuinely behind this binary, bring it up:"+
		"\n    go get %s@%s && go mod tidy"+
		"\n  (and re-run both in gen/ if the project has one). "+
		"If the two toolchains below disagree, the pin is not the problem — re-run with the SAME "+
		"forge that generated this tree, or reinstall both so they match",
		forgeModuleRequirePath, binaryVersion)

	// A pseudo-version names a COMMIT, and a proxy can only serve it once
	// that commit is pushed. Telling someone to `go get` an unpushed one sends
	// them to "unknown revision" — the same class of mistake as pinning a
	// version that cannot satisfy the generated code, which is what this whole
	// check exists to prevent. We cannot know from here whether it is pushed
	// (that needs the network, or knowledge of the remote), so say so and name
	// the alternative rather than asserting a ref that may not exist.
	if module.IsPseudoVersion(binaryVersion) {
		fix += fmt.Sprintf("\n  NOTE: %s is a pseudo-version — it names commit %s, and `go get` can only "+
			"resolve it once that commit is PUSHED. If it is not, bridge to the source instead:"+
			"\n    go work init . && go work use . <path-to-your-forge-checkout>",
			binaryVersion, shortPseudoCommit(binaryVersion))
	}

	base := cliutil.UserErr("forge generate (forge version compatibility)",
		fmt.Sprintf("this forge is %s but the project pins %s %s — older than the binary generating into "+
			"it, so the generated code may call symbols that release does not have. Generating would "+
			"rewrite the tree and then fail its own validate",
			binaryVersion, forgeModuleRequirePath, projectVersion),
		"", fix)

	return fmt.Errorf("%w\n\n%s", base, toolchainDiagnosis(projectDir))
}

// forgeInvocationArgs is the forge arguments of THIS invocation — what the
// user typed after `forge` (or after `reliant forge` when forge is mounted
// inside another binary) — so a refusal can hand back the same command, run
// with a different forge.
func forgeInvocationArgs() []string {
	if len(os.Args) < 2 {
		return nil
	}
	args := os.Args[1:]
	if route, ok := cmdutil.CmdRouteTokens(); ok && len(route) <= len(args) {
		matches := true
		for i, tok := range route {
			if args[i] != tok {
				matches = false
				break
			}
		}
		if matches {
			args = args[len(route):]
		}
	}
	return args
}

// shortPseudoCommit pulls the commit out of a pseudo-version for the message,
// degrading to the whole version string if the shape is unexpected — a
// diagnostic must never be the thing that fails.
func shortPseudoCommit(v string) string {
	if rev, err := module.PseudoVersionRev(v); err == nil && rev != "" {
		return rev
	}
	return v
}

// retiredPkgRequire is one of the project's own go.mod files that directly
// requires the retired forge/pkg module.
type retiredPkgRequire struct {
	// Dir is the module directory relative to the project root ("." or "gen").
	Dir     string
	Version string
}

// projectRetiredPkgRequires reads the project's own root go.mod for a direct
// retired require. gen/go.mod is deliberately NOT read: it is forge's module,
// and the "ensure gen/go.mod" step migrates it in place rather than refusing —
// the user's go.mod is the only one whose swap forge cannot make for them.
func projectRetiredPkgRequires(_, rootGoMod string) []retiredPkgRequire {
	if v, found := directRetiredPkgRequire(rootGoMod); found {
		return []retiredPkgRequire{{Dir: ".", Version: v}}
	}
	return nil
}

// retiredPkgModuleErr names the pre-collapse submodule. Only the DIRECT case
// reaches here — see directRetiredPkgRequire for why forge does not go looking
// for it in the graph.
func retiredPkgModuleErr(projectDir string, retired []retiredPkgRequire) error {
	// The replacement must be a version the proxy SERVES: this binary's own
	// when it can name one, else the newest release its source descends from.
	// "latest" would do for a released binary but can lag a dev build's floor.
	target := buildinfo.InstallableVersion()
	if target == "" {
		target = buildinfo.PublishedFloor()
	}
	if target == "" {
		target = "latest"
	}

	var where []string
	for _, r := range retired {
		file := "go.mod"
		if r.Dir != "." {
			file = r.Dir + "/go.mod"
		}
		where = append(where, fmt.Sprintf("%s (%s)", file, r.Version))
	}
	what := fmt.Sprintf("this project requires the retired module github.com/reliant-labs/forge/pkg in %s. "+
		"It was merged into github.com/reliant-labs/forge, and BOTH modules provide the import path "+
		"github.com/reliant-labs/forge/pkg/* — so every such import is ambiguous and nothing in the "+
		"project compiles. Import paths did NOT change; only the require line did", strings.Join(where, " and "))

	// One command line per affected module, each runnable from the project
	// root as written. Requiring github.com/reliant-labs/forge explicitly is
	// what keeps it from coming back: `go mod tidy` ignores go.work, so a
	// module that imports forge/pkg/* WITHOUT a forge require makes tidy pick
	// the retired module again as the longest path that provides it.
	var fix strings.Builder
	fix.WriteString("swap the requirement in each affected module (run from the project root):")
	for _, r := range retired {
		fmt.Fprintf(&fix, "\n    (cd %s && go mod edit -droprequire=github.com/reliant-labs/forge/pkg && "+
			"go get github.com/reliant-labs/forge@%s && go mod tidy)", r.Dir, target)
	}
	fix.WriteString("\n  then re-run generate")

	base := cliutil.UserErr("forge generate (forge version compatibility)", what, "", fix.String())
	return fmt.Errorf("%w\n\n%s", base, toolchainDiagnosis(projectDir))
}
