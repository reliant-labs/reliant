package cli

// Is the running forge the forge this project pins?
//
// WHY THIS IS LOUD. On 2026-10-07 ~/go/bin/forge was 3b4499b3 while every repo
// pinned d6b5d722, and agents repeatedly ran the wrong binary: fixes that were
// committed and pinned "did not work" because the forge running them predated
// them. Nothing said so. forgeVersionMismatchWarning stays silent for
// pseudo-versions on purpose (a dev build is not a release), and every pin in
// this workspace IS a pseudo-version — so the one comparison that mattered was
// the one never made.
//
// The comparison here is by IDENTITY, which a pseudo-version carries exactly:
// its 12-hex commit. Two pseudo-versions of one commit are the same forge;
// two commits are not, whatever their timestamps say.
//
// Every project command warns. The commands that BUILD, RELEASE or DEPLOY
// refuse, because what they produce is a function of the binary (a bundle
// records the forge that rendered it), and an artifact made by an unpinned
// forge is one the project cannot reproduce. So do the commands that
// REWRITE the project's files from the binary's templates (generate,
// scaffold, ...): 2026-10-12, two agents ran `forge
// generate` under a stale forge on PATH and it silently rewrote unrelated
// generated files (otel, frontend, descriptor) until typecheck broke.
// --allow-version-skew (or FORGE_ALLOW_VERSION_SKEW=1) proceeds anyway.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/forgecompat"
	"github.com/reliant-labs/forge/internal/forgedelegate"
)

// pinGatedCommands are the commands, relative to forge's root, that refuse
// to run under a forge other than the pinned one. The value lists boolean
// flags that make a run read-only (a plan, a dry run, a check): when one is
// set the command is not refused, because nothing is written. The build and
// deploy commands list none — their plan/explain output is itself a function
// of the binary and is gated like the real run.
var pinGatedCommands = map[string][]string{
	"env deploy": nil,
	"env build":  nil,
	"build":      nil,

	// Commands that rewrite the project's files from this binary's templates.
	// `lint` is deliberately absent: its auto-fix pass runs gofmt, golangci,
	// prettier and eslint, whose output does not depend on forge's templates.
	"generate":           {"dry-run", "check"},
	"package new":        nil,
	"component install":  nil,
	"env new":            {"check"},
	"project rescaffold": nil,
	"project disown":     {"dry-run"},
}

// pinBuildCommands are the gated commands whose product is an artifact, not
// an edit to the tree. They are strict about every skew, including a
// development binary against a project pinned to a published tag. The
// rewriting commands tolerate that one pairing: it is how forge's own e2e
// lanes and every forge developer scaffold and generate (a binary built from
// the checkout, a project on the published floor), and generate already
// judges it with its own compatibility verdicts (generate_pkg_compat.go).
var pinBuildCommands = map[string]bool{
	"env deploy": true,
	"env build":  true,
	"build":      true,
}

// pinGatedGroups are command groups whose every subcommand mutates the
// project, so a subcommand added later is gated without anyone remembering
// to list it. Subcommands of a group honour the same read-only flags.
var pinGatedGroups = map[string][]string{
	"scaffold":        {"dry-run"},
	"project delete":  {"dry-run"},
	"project migrate": {"dry-run"},
}

// pinGateFor reports whether rel (a command path relative to forge's root)
// is pin-gated, and the flags that make a run of it read-only.
func pinGateFor(rel string) (readOnlyFlags []string, gated bool) {
	if flags, ok := pinGatedCommands[rel]; ok {
		return flags, true
	}
	for group, flags := range pinGatedGroups {
		if rel == group || strings.HasPrefix(rel, group+" ") {
			return flags, true
		}
	}
	return nil, false
}

// readOnlyRun reports whether cmd was invoked with one of the gate's
// read-only flags.
func readOnlyRun(cmd *cobra.Command, readOnlyFlags []string) bool {
	for _, name := range readOnlyFlags {
		if cmd.Flags().Lookup(name) == nil {
			continue
		}
		if on, _ := cmd.Flags().GetBool(name); on {
			return true
		}
	}
	return false
}

// allowVersionSkewEnv proceeds past the refusal, like --allow-version-skew.
const allowVersionSkewEnv = "FORGE_ALLOW_VERSION_SKEW"

// allowVersionSkewFlag is registered on exactly the pin-gated commands (it
// means nothing anywhere else) by registerVersionSkewFlags.
const allowVersionSkewFlag = "allow-version-skew"

// registerVersionSkewFlags adds --allow-version-skew to each pin-gated
// command under root.
func registerVersionSkewFlags(root *cobra.Command) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		rel := strings.TrimSpace(strings.TrimPrefix(c.CommandPath(), root.CommandPath()))
		if _, gated := pinGateFor(rel); gated && c.Flags().Lookup(allowVersionSkewFlag) == nil {
			c.Flags().Bool(allowVersionSkewFlag, false,
				"Run even though this forge is not the forge_version forge.yaml pins (also "+allowVersionSkewEnv+"=1)")
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

// pseudoCommitRE captures a Go pseudo-version's 12-hex commit.
var pseudoCommitRE = regexp.MustCompile(`-(?:[\w.]+\.)?[0-9]{14}-([0-9a-f]{12})(?:\+.*)?$`)

// forgeIdentity is what a version string says about WHICH forge it is: a
// commit for a pseudo-version, the version itself (without build metadata)
// for a tag. Empty when the string names no forge in particular.
func forgeIdentity(v string) string {
	v = strings.TrimSpace(v)
	switch v {
	case "", "dev", "(devel)":
		return ""
	}
	if m := pseudoCommitRE.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	if i := strings.Index(v, "+"); i >= 0 {
		v = v[:i]
	}
	return v
}

// pinSkew reports whether binary (and, when its version names no commit,
// binaryCommit) is a different forge from pin. ok is false when either side
// names no forge in particular — nothing can be compared, and nothing is said.
func pinSkew(pin, binary, binaryCommit string) (skewed, ok bool) {
	want := forgeIdentity(pin)
	have := forgeIdentity(binary)
	if pseudoCommitRE.MatchString(strings.TrimSpace(pin)) && !pseudoCommitRE.MatchString(strings.TrimSpace(binary)) && len(binaryCommit) >= 12 {
		have = binaryCommit[:12]
	}
	if want == "" || have == "" {
		return false, false
	}
	return want != have, true
}

// pinSkewFor reports the pin and the running binary's version when they are
// different forges (devAgainstTag: a development binary against a pin that
// is a published tag), or skewed=false when nothing is to be said: no project,
// forge's own repository, a project bridged to the checkout this binary was
// built from, a replaced (local) forge, or a side that names no forge.
func pinSkewFor(projectRoot string) (pin, binary string, skewed, devAgainstTag bool) {
	cfg, ok := readPinFields(projectRoot)
	if !ok {
		return "", "", false, false
	}
	// forge's own repository is built and run from source by the people
	// changing it; the pin there describes the scaffolds, not the binary.
	if cfg.ModulePath == forgeModulePathForSkew {
		return "", "", false, false
	}
	build := buildinfo.Current()
	// `replace github.com/reliant-labs/forge => ../forge` in the embedding
	// binary: the running forge is a local checkout, not a version.
	if build.ReplacedBy != "" {
		return "", "", false, false
	}
	// A project bridged (go.work) to the local forge checkout this very
	// binary was built from is someone running the forge they are working
	// on — the e2e lanes, a forge developer. Its pin follows go.mod (often
	// the published floor) and says nothing about that binary. A bridged
	// project run by a binary that is NOT its checkout's — a stale
	// ~/go/bin/forge, the 2026-10-07 case — is checked like any other.
	if rep := forgecompat.InspectBridge(projectRoot); rep.Bridged && rep.CheckoutOK && !rep.Skewed {
		return "", "", false, false
	}
	// build.Commit, not buildinfo.GitCommit(): the latter falls back to the
	// vcs revision of the HOST binary when forge is embedded (`reliant
	// forge`), which would compare reliant's commit against forge's pin.
	pin, binary = cfg.ForgeVersion, buildinfo.Version()
	skew, ok := pinSkew(pin, binary, build.Commit)
	if !ok || !skew {
		return "", "", false, false
	}
	devAgainstTag = !buildinfo.IsDevVersion(pin) && buildinfo.IsDevVersion(binary)
	return pin, binary, true, devAgainstTag
}

// pinFields is the slice of forge.yaml the pin comparison needs.
type pinFields struct {
	ModulePath   string `yaml:"module_path"`
	ForgeVersion string `yaml:"forge_version"`
}

// readPinFields reads forge_version (and module_path) from projectRoot's
// forge.yaml WITHOUT validating the rest of the file.
//
// Validating first is how a newer project locked out an older binary: forge
// 25283f30 made module_path optional for a site with no go.mod, and a forge
// from before it rejected that forge.yaml ("'module_path' is required")
// before it ever read the pin — so it never handed the command to the forge
// that accepts the file. What this binary thinks of the rest of forge.yaml is
// irrelevant when the project pins another forge: that one judges it.
func readPinFields(projectRoot string) (pinFields, bool) {
	data, err := os.ReadFile(filepath.Join(projectRoot, defaultProjectConfigFile))
	if err != nil {
		return pinFields{}, false
	}
	var f pinFields
	if yaml.Unmarshal(data, &f) != nil {
		return pinFields{}, false
	}
	return f, true
}

// pinSkewRefusal is the runbook printed when a command is refused: which
// forge is running, which the project pins, and the literal commands that fix it.
func pinSkewRefusal(projectRoot, pin, binary, what string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "this forge is %s, but %s/forge.yaml pins forge_version %s.\n", binary, projectRoot, pin)
	fmt.Fprintf(&b, "  `%s` rewrites or produces files from the binary's own templates, so it runs only under the pinned forge —\n", what)
	b.WriteString("  a different one silently rewrites unrelated generated files.\n")
	writePinSkewRemedy(&b, "  ", projectRoot, pin, buildinfo.Current())
	fmt.Fprintf(&b, "  If the PIN is what is wrong, move it deliberately: `%s project upgrade`.\n", Name())
	fmt.Fprintf(&b, "  To proceed anyway, knowingly (forge developers): --%s, or %s=1", allowVersionSkewFlag, allowVersionSkewEnv)
	return fmt.Errorf("%s", b.String())
}

// checkForgePinSkew is the root command's check, for cmd. A command that
// depends on which forge ran it is handed to the forge the project wants
// (forge_pin_delegate.go); when that is not possible, or the command is not
// one of those, it warns on w, and returns a refusal for a pin-gated command
// unless allowed.
func checkForgePinSkew(cmd, root *cobra.Command, w io.Writer, allow bool) error {
	if forgedelegate.Delegated() {
		return nil
	}
	projectRoot, err := cmdutil.FindProjectRoot()
	if err != nil || projectRoot == "" {
		return nil
	}
	rel := strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), root.CommandPath()))
	readOnlyFlags, gated := pinGateFor(rel)
	readOnly := readOnlyRun(cmd, readOnlyFlags)
	overridden := allow || forgecompat.SkewOverridden()

	var delegateErr error
	if !overridden && delegationWanted(rel, readOnly) && !pinDelegationDisabled {
		if t, ok := delegateTargetFor(projectRoot); ok && !t.devAgainstTag(rel) {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			handed, err := delegateToPinned(ctx, w, projectRoot, t, forgeInvocationArgs())
			if handed {
				return nil
			}
			delegateErr = err
		}
	}

	pin, binary, skewed, devAgainstTag := pinSkewFor(projectRoot)
	if !skewed {
		return nil
	}
	if gated && !readOnly && !(devAgainstTag && !pinBuildCommands[rel]) && !overridden {
		err := pinSkewRefusal(projectRoot, pin, binary, "forge "+rel)
		if note := delegationFailure(delegateErr); note != "" {
			return fmt.Errorf("%s\n%s", err, strings.TrimSuffix(note, "\n"))
		}
		return err
	}
	_, _ = fmt.Fprint(w, pinSkewWarning(projectRoot, pin, binary, buildinfo.Current()))
	if note := delegationFailure(delegateErr); note != "" {
		_, _ = fmt.Fprint(w, "  "+strings.TrimPrefix(note, "  "))
	}
	return nil
}

// pinSkewWarning is what a command that is not refused prints under skew. It
// gives the refusal's remedy: an embedded forge (`reliant forge`) is not
// changed by installing forge, so "go install" alone sent people in a circle.
func pinSkewWarning(projectRoot, pin, binary string, cur buildinfo.Build) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⚠️  this forge is %s, but forge.yaml pins forge_version %s — a different forge than the project's.\n", binary, pin)
	writePinSkewRemedy(&b, "   ", projectRoot, pin, cur)
	return b.String()
}

// writePinSkewRemedy writes how to run the pinned forge instead of this one,
// each line prefixed by indent. Running it with `go run` works everywhere;
// installing it helps only a standalone forge, so an embedded one is told to
// upgrade its host instead.
func writePinSkewRemedy(b *strings.Builder, indent, projectRoot, pin string, cur buildinfo.Build) {
	fmt.Fprintf(b, "%sRun it with the pinned forge, installing nothing:\n", indent)
	fmt.Fprintf(b, "%s    %s\n", indent, pinnedRunLine(projectRoot, pin, forgeInvocationArgs()))
	if !cur.Embedded {
		fmt.Fprintf(b, "%sor install the pinned forge (from outside the project's module):\n", indent)
		fmt.Fprintf(b, "%s    cd \"$(mktemp -d)\" && GOPRIVATE='github.com/reliant-labs/*' go install github.com/reliant-labs/forge/cmd/forge@%s\n", indent, pin)
		return
	}
	host := cur.Embedder
	if i := strings.LastIndex(host, "/"); i >= 0 {
		host = host[i+1:]
	}
	if host == "" {
		host = "the binary embedding it"
	}
	fmt.Fprintf(b, "%sThis forge is embedded in %s, so installing forge does not change `%s`:\n", indent, host, Name())
	fmt.Fprintf(b, "%suse the `go run` form above, or upgrade %s so it embeds the pinned forge.\n", indent, host)
}

// refuseGenerateUnderPinSkew is the check for the paths that run the
// generate pipeline on the user's behalf without being the `generate`
// command (ensureGeneratedCode, reached from `forge env up`): the pipeline
// rewrites the tree exactly as `forge generate` would, so it needs the same
// guard. Only the environment variable can lift it there — the callers have
// no flag of their own; --no-generate skips the pipeline instead.
func refuseGenerateUnderPinSkew(projectRoot string) error {
	if forgecompat.SkewOverridden() {
		return nil
	}
	pin, binary, skewed, devAgainstTag := pinSkewFor(projectRoot)
	if !skewed || devAgainstTag {
		return nil
	}
	return pinSkewRefusal(projectRoot, pin, binary, "forge generate (run automatically because generated code is stale)")
}

// forgeModulePathForSkew is forge's own module path.
const forgeModulePathForSkew = "github.com/reliant-labs/forge"
