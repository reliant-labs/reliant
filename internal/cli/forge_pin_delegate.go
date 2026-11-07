package cli

// Run the forge the project pins.
//
// forge_pin_skew.go decides that the forge in hand is not the project's. This
// file is what happens next: instead of stopping with a runbook, hand the very
// same command to the pinned forge. The project declares which forge it is
// (forge.yaml's forge_version, or a go.mod/go.work link to a checkout), the
// declaration is enough to act on, and making every agent type
// `go run …@<pin>` by hand was a wall with a door a program could open.
//
// WHICH COMMANDS: every command run inside a project that pins another forge,
// except the few that are about THIS forge or this machine rather than the
// project (handOffExempt). Reads included: `skill load`, `project shapes` and
// a `generate --dry-run` plan are all functions of the binary, and an answer
// from the wrong forge is a wrong answer that looks right. Forge report 86c6fd64: an
// agent's `reliant forge generate` in a project pinned to v1.0.4 ran the forge
// reliant embedded (a v0.1.47 pseudo-version) and rewrote hundreds of
// generated lines, because that pairing was tolerated as "a development forge".
//
// WHICH forge, in order:
//   - a project linked to a local forge checkout (go.work use, directory
//     replace) compiles against that checkout, so the forge it runs is a build
//     of that checkout — when this binary was not built from it;
//   - otherwise the forge_version pin: the project's .tools/bin/forge when it
//     is that version, else a build of the pinned module version kept in a
//     content-addressed cache (internal/forgedelegate).
//
// WHEN the decision is made: before the command tree parses anything, when
// the entrypoint calls HandOffToPinned (cmd/forge through Execute, and a host
// that embeds forge, such as `reliant forge`, through cli.HandOffToPinned).
// That is the only place a command THIS forge does not have — one the pinned
// forge added — can still reach the forge that has it. The root's
// PersistentPreRun makes the same decision for a host that did not call it.
//
// The hand-off is an exec: same arguments, same environment (plus
// FORGE_VERSION_DELEGATED, which stops the pinned forge handing off again —
// that forge alone: it is addressed to the exec'd pid and consumed at start-up,
// so nothing the delegate starts inherits it; see forgedelegate.MarkerEnv),
// stdio and exit status. One line on stderr says it happened.
//
// It is skipped when --allow-version-skew or FORGE_ALLOW_VERSION_SKEW is set
// (the explicit "run THIS forge"; the skew warning and refusals then apply),
// when this forge is already a delegate, and when this forge is a local build
// of forge being run against a published-tag pin (devAgainstTag). When the
// pinned forge cannot be had — offline, an unpublished version, a checkout
// that does not build — the command FAILS with the reason. It never falls back
// to running this forge: that fallback is how a stale forge rewrote a project.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/forgecompat"
	"github.com/reliant-labs/forge/internal/forgedelegate"
)

// handOffExempt are the commands that run as THIS forge in any project: they
// describe or operate this binary or this machine, not the project.
//
//   - version, help, completion: about this binary and this shell.
//   - project upgrade: moves the pin, so it is run by the forge being moved
//     TO, and must work when the old pin cannot be built.
//   - project new: the project it writes is not the one around the cwd.
//   - report-bug: reports a defect in this forge, and must work offline.
//   - storage, slot: bound this machine's caches and heavy slots under THIS
//     forge's rules. A pinned forge older than the 2h floor and the
//     live-build lease (#1231, #1243, #1245) would trim the Go build cache
//     down to now-20m, under running builds.
//   - mcp: a long-lived server whose every tool call names its own project
//     (each may pin a different forge), run in-process. Handing the server
//     to the forge its STARTING directory pins would make every later
//     request run under that forge, past its own pin check (a delegate does
//     not check). Each request is checked against its own project instead
//     (serveRequestsUnderTheirOwnPins).
//   - env ps, env down, env devstack: they list, stop and allocate ports for
//     the stacks on this machine, across projects, and stopping one must not
//     need the network.
var handOffExempt = []string{
	"", "version", "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd,
	"project upgrade", "project new", "report-bug", "storage", "slot", "mcp",
	"env ps", "env down", "env devstack",
}

// handOffWanted reports whether the command at rel (relative to forge's root)
// is handed to the forge a project pins. rel "" is the root itself.
func handOffWanted(rel string) bool {
	for _, ex := range handOffExempt {
		if rel == ex || (ex != "" && strings.HasPrefix(rel, ex+" ")) {
			return false
		}
	}
	return true
}

// Seams: the real hand-off builds forge and replaces the process, which no
// unit test may do (main_test.go installs hermetic defaults).
var (
	pinDelegateResolver = func(w io.Writer) *forgedelegate.Resolver {
		return &forgedelegate.Resolver{
			Log:     w,
			Version: forgedelegate.RunVersion,
			Same: func(want, got string) bool {
				skewed, ok := pinSkew(want, got, "")
				return ok && !skewed
			},
		}
	}
	pinDelegateExec = forgedelegate.Exec
	// isDelegate: this process is the forge a hand-off exec'd. Decided once,
	// at start-up (forgedelegate consumes the marker before main), so the
	// tests that play a delegate swap this instead of the environment.
	isDelegate = forgedelegate.Delegated
	// forgeIsLocalBuild: only a forge built from a local tree is "a
	// development forge" a rewriting command tolerates against a tag pin.
	forgeIsLocalBuild = buildinfo.LocalBuild
	// forgeEmbedding names the host binary forge is compiled into ("" when
	// forge is its own executable), for the no-pin notice.
	forgeEmbedding = func() (host, version string) {
		cur := buildinfo.Current()
		if !cur.Embedded {
			return "", ""
		}
		host = cur.Embedder
		if i := strings.LastIndex(host, "/"); i >= 0 {
			host = host[i+1:]
		}
		if host == "" {
			host = "its host binary"
		}
		return host, cur.Version
	}
)

// pinDelegationDisabled is set by main_test.go's TestMain: a real hand-off
// builds forge and replaces the process, neither of which a unit test may do.
// The tests of the hand-off itself clear it and swap the two seams above.
var pinDelegationDisabled bool

// handOffDecided is set by HandOffToPinned: the entrypoint made the decision
// before the command tree ran, so the root's PersistentPreRun does not make it
// (and print its notices) a second time.
var handOffDecided bool

// HandOffToPinned runs args — forge's own arguments, after any host route
// such as `reliant forge` — under the forge the project in scope pins, when
// that is not this forge. It is called before the command tree parses args.
//
// With the real exec it returns only when nothing was handed over (handed
// false, err nil: run this forge) or on failure (err: the pinned forge is
// wanted and could not be had; nothing may run). stderr receives the one-line
// notices.
func HandOffToPinned(ctx context.Context, args []string, stderr io.Writer) (handed bool, err error) {
	handOffDecided = true
	if isDelegate() || pinDelegationDisabled {
		return false, nil
	}
	inv := parseInvocation(args)
	if !handOffWanted(inv.rel) {
		return false, nil
	}
	root, err := projectRootFrom(inv.projectDir)
	if err != nil {
		// The tree reports a bad -C itself, with its own message.
		return false, nil
	}
	return handOff(ctx, stderr, inv.rel, inv.allow, root, args)
}

// invocation is what the hand-off needs from a command line it has not run.
type invocation struct {
	// rel is the command path relative to forge's root; for a command this
	// forge does not know, the path up to and including the unknown word.
	rel string
	// allow is --allow-version-skew.
	allow bool
	// projectDir is -C / --project-dir.
	projectDir string
}

// parseInvocation resolves args against a throwaway copy of this forge's
// command tree, the way cobra will, without running anything. Unknown flags
// are skipped rather than fatal: a flag the pinned forge added is exactly the
// case the hand-off exists for.
func parseInvocation(args []string) invocation {
	root := NewRootCmd()
	cmd, rest, err := root.Find(args)
	if err != nil || cmd == nil {
		// An unknown top-level command: this forge cannot run it, the
		// pinned one may.
		return invocation{rel: firstCommandWord(root, args), projectDir: scanProjectDir(args)}
	}
	cmd.FParseErrWhitelist.UnknownFlags = true
	_ = cmd.ParseFlags(rest)
	inv := invocation{rel: strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), root.CommandPath()))}
	inv.allow, _ = cmd.Flags().GetBool(allowVersionSkewFlag)
	inv.projectDir, _ = cmd.Flags().GetString("project-dir")
	if cmd.HasSubCommands() {
		// `forge project <a subcommand this forge lacks>`.
		if w := firstWord(cmd.Flags().Args()); w != "" {
			inv.rel = strings.TrimSpace(inv.rel + " " + w)
		}
	}
	return inv
}

// firstWord is the first argument that is not a flag. For positionals cobra
// has already parsed (no flag values among them).
func firstWord(args []string) string {
	for _, a := range args {
		if a == "--" {
			return ""
		}
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// firstCommandWord is the command a raw command line names: its first
// argument that is neither a flag nor the value of one. A flag of root's that
// takes a value consumes the next argument, as cobra parses it — so
// `forge -C version future-command` names future-command, not the exempt
// `version` (a project directory that happens to be called that). An unknown
// flag is taken to stand alone: this forge cannot know its arity.
func firstCommandWord(root *cobra.Command, args []string) string {
	takesValue := func(name string, short bool) bool {
		var f *pflag.Flag
		if short {
			f = root.PersistentFlags().ShorthandLookup(name)
			if f == nil {
				f = root.Flags().ShorthandLookup(name)
			}
		} else if f = root.PersistentFlags().Lookup(name); f == nil {
			f = root.Flags().Lookup(name)
		}
		return f != nil && f.NoOptDefVal == ""
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return ""
		case strings.HasPrefix(a, "--"):
			if !strings.Contains(a, "=") && takesValue(strings.TrimPrefix(a, "--"), false) {
				i++
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// -C x takes the next argument; -Cx and -C=x carry it.
			if len(a) == 2 && takesValue(a[1:], true) {
				i++
			}
		default:
			return a
		}
	}
	return ""
}

// scanProjectDir finds -C / --project-dir in args the tree could not parse,
// as cobra would read it: every occurrence up to `--`, the LAST one winning.
// Taking the first sent `forge <unknown> -C a -C b` to a's pinned forge with
// arguments that select b in the receiver.
func scanProjectDir(args []string) string {
	dir := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return dir
		case a == "-C" || a == "--project-dir":
			if i+1 < len(args) {
				dir = args[i+1]
				i++ // the value is not a flag, whatever it looks like
			}
		case strings.HasPrefix(a, "--project-dir="):
			dir = strings.TrimPrefix(a, "--project-dir=")
		case strings.HasPrefix(a, "-C") && len(a) > 2:
			dir = strings.TrimPrefix(strings.TrimPrefix(a, "-C"), "=")
		}
	}
	return dir
}

// projectRootFrom is the project root THIS invocation names: the nearest
// forge.yaml at or above dir (-C), or above the working directory when dir is
// ""; "" outside any project.
//
// It reads no process-global state and installs none. An in-process embedder
// (reliant) runs many invocations in one process, and the root's
// PersistentPreRun leaves the last one's -C installed in cmdutil; resolving
// through that override handed a command to the PREVIOUS invocation's
// project's pinned forge, and the delegate's marker then disabled the
// receiver's own check. The answer is a function of this invocation's
// arguments and working directory alone.
func projectRootFrom(dir string) (string, error) {
	start := dir
	if start == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		start = wd
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return "", fmt.Errorf("project dir %q is not a directory", dir)
	}
	for d := abs; ; d = filepath.Dir(d) {
		if st, err := os.Stat(filepath.Join(d, defaultProjectConfigFile)); err == nil && !st.IsDir() {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", nil // no project is an answer, not a failure
		}
	}
}

// handOff is the decision both entry points share: hand the command at rel to
// the forge projectRoot wants, run this forge, or fail. args are the forge
// arguments of this invocation.
func handOff(ctx context.Context, w io.Writer, rel string, allow bool, projectRoot string, args []string) (handed bool, err error) {
	if !handOffWanted(rel) {
		return false, nil
	}
	t, ok, unpinned := pinTarget(rel, allow, projectRoot)
	if unpinned != "" {
		noteUnpinned(w, unpinned)
	}
	if !ok {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	handed, err = delegateToPinned(ctx, w, projectRoot, t, args)
	if err != nil {
		return false, handOffFailure(projectRoot, t, rel, err)
	}
	return handed, nil
}

// pinTarget is the forge the command at rel must run under in projectRoot,
// when that is not this forge (ok). ok is false when this forge runs it: no
// project or no pin (unpinned says which, for the notice), no skew, the user
// overrode the check (allow, or the environment), or a local development forge
// against a tag pin for a command that tolerates it.
func pinTarget(rel string, allow bool, projectRoot string) (t delegateTarget, ok bool, unpinned string) {
	if projectRoot == "" {
		return delegateTarget{}, false, "not inside a forge project"
	}
	if cfg, ok := readPinFields(projectRoot); ok && strings.TrimSpace(cfg.ForgeVersion) == "" && !forgecompat.InspectBridge(projectRoot).Bridged {
		return delegateTarget{}, false, filepath.Join(projectRoot, defaultProjectConfigFile) + " pins no forge_version"
	}
	t, ok = delegateTargetFor(projectRoot)
	if !ok {
		return delegateTarget{}, false, ""
	}
	if allow || forgecompat.SkewOverridden() || t.devAgainstTag(rel) {
		// Run this forge; checkForgePinSkew warns, or refuses.
		return delegateTarget{}, false, ""
	}
	return t, true, ""
}

// PinnedForge is the forge a project pins, resolved and ready to run.
type PinnedForge struct {
	// Path is the executable.
	Path string
	// Key identifies exactly which forge it is, for a cache of what it
	// prints: the pinned version, or a linked checkout and its contents.
	Key string
	// Want says which forge the project wants, for a message.
	Want string
}

// ResolvePinnedForge is the hand-off's decision and resolution without the
// exec, for a host that runs the project's forge itself (reliant reading the
// pinned forge's skills). command is the forge command it will run ("skill
// load"), which the decision depends on. ok is false when this forge is the
// one to run it; err, when the pinned forge is wanted and cannot be obtained.
// log receives the one-time "building forge" notice.
func ResolvePinnedForge(ctx context.Context, projectRoot, command string, log io.Writer) (PinnedForge, bool, error) {
	if isDelegate() || !handOffWanted(command) {
		return PinnedForge{}, false, nil
	}
	t, ok, _ := pinTarget(command, false, projectRoot)
	if !ok {
		return PinnedForge{}, false, nil
	}
	res, err := resolveTarget(ctx, log, projectRoot, t)
	if err != nil {
		return PinnedForge{}, false, handOffFailure(projectRoot, t, command, err)
	}
	return PinnedForge{Path: res.Path, Key: t.key(), Want: t.String()}, true, nil
}

// noteUnpinned says which forge runs when no project pin decides it. Only an
// embedded forge says it: a standalone forge is the binary the user chose,
// while the forge inside `reliant forge` is whatever that host was built
// with, and nothing else on screen names it.
func noteUnpinned(w io.Writer, why string) {
	host, version := forgeEmbedding()
	if host == "" {
		return
	}
	_, _ = fmt.Fprintf(w, "%s: %s — running the forge built into %s, %s\n", Name(), why, host, version)
}

// handOffFailure is the error when the project's forge is wanted and cannot
// be had. Nothing runs: running this forge instead is the silent rewrite the
// hand-off exists to prevent.
func handOffFailure(projectRoot string, t delegateTarget, rel string, cause error) error {
	var b strings.Builder
	fmt.Fprintf(&b, "this forge is %s, but %s/forge.yaml wants %s, and that forge could not be obtained:\n", t.binary, projectRoot, t)
	for _, line := range strings.Split(strings.TrimSpace(cause.Error()), "\n") {
		fmt.Fprintf(&b, "    %s\n", line)
	}
	fmt.Fprintf(&b, "  Nothing ran: `%s` runs only under the forge the project pins, never silently under another.\n", strings.TrimSpace(Name()+" "+rel))
	fmt.Fprintf(&b, "  Fix what stopped it (offline? a pin nobody published?), move the pin deliberately with `%s project upgrade`,\n", Name())
	fmt.Fprintf(&b, "  or run THIS forge knowingly: --%s, or %s=1", allowVersionSkewFlag, allowVersionSkewEnv)
	return errors.New(b.String())
}

// delegateTarget is what the project says its forge is.
type delegateTarget struct {
	// pin is forge.yaml's forge_version (the target when checkout is "").
	pin string
	// binary is the running forge's version, for the notice.
	binary string
	// checkout, when set, is the local forge checkout the project is linked
	// to, and stamp identifies its current contents.
	checkout, stamp string
	// devTag: the running forge is a local development build and the pin a
	// published tag (see pinSkewFor).
	devTag bool
}

// devAgainstTag reports whether the pairing is one the rewriting commands
// tolerate (forge's own e2e lanes and every forge developer): a binary built
// from a local checkout, a project on the published floor. Only the commands
// that produce artifacts are strict about it.
func (t delegateTarget) devAgainstTag(rel string) bool {
	return t.devTag && !pinBuildCommands[rel]
}

// key identifies exactly the forge t names: a pin names one module version,
// a checkout's stamp its current contents.
func (t delegateTarget) key() string {
	if t.checkout != "" {
		return "checkout:" + t.checkout + "@" + t.stamp
	}
	return "pin:" + t.pin
}

func (t delegateTarget) String() string {
	if t.checkout != "" {
		return "the checkout " + t.checkout + " this project is linked to"
	}
	return "forge_version " + t.pin
}

// delegateTargetFor reports the forge projectRoot wants and whether this one
// is not it. ok is false when nothing is to be done: no project, no skew, or
// a comparison that cannot be made.
func delegateTargetFor(projectRoot string) (t delegateTarget, ok bool) {
	if rep := forgecompat.InspectBridge(projectRoot); rep.Bridged && rep.CheckoutOK {
		if !rep.Skewed {
			return delegateTarget{}, false
		}
		return delegateTarget{
			binary:   rep.Binary.Name,
			checkout: rep.Bridge.Dir,
			// Keyed on source edits only: an untracked report growing in the
			// checkout's root must not rebuild forge on every command.
			stamp: rep.Checkout.Head + "+" + strconv.FormatBool(rep.Checkout.SourceEdits) + "@" + strconv.FormatInt(rep.Checkout.LastSourceEditAt.UnixNano(), 10),
		}, true
	}
	pin, binary, skewed, devTag := pinSkewFor(projectRoot)
	if !skewed {
		return delegateTarget{}, false
	}
	return delegateTarget{pin: pin, binary: binary, devTag: devTag}, true
}

// delegateToPinned hands the running command to the forge projectRoot wants.
// With the real Exec it returns only on failure; handed is true when the
// command was handed over and nothing more is to be done here (a test seam
// returns instead of replacing the process). args are the forge arguments of
// this invocation.
func delegateToPinned(ctx context.Context, w io.Writer, projectRoot string, t delegateTarget, args []string) (handed bool, err error) {
	res, err := resolveTarget(ctx, w, projectRoot, t)
	if err != nil {
		return false, err
	}
	_, _ = fmt.Fprintf(w, "forge: this forge is %s, but the project wants %s — running %s instead (%s, or %s=1, to run this one)\n",
		t.binary, t, res.Source, "--"+allowVersionSkewFlag, allowVersionSkewEnv)
	if err := pinDelegateExec(res.Path, args, forgedelegate.Env(t.String())); err != nil {
		return false, err
	}
	return true, nil
}

// resolveTarget finds, or builds, the forge t names.
func resolveTarget(ctx context.Context, w io.Writer, projectRoot string, t delegateTarget) (forgedelegate.Result, error) {
	r := pinDelegateResolver(w)
	if t.checkout != "" {
		return r.Bridge(ctx, t.checkout, t.stamp)
	}
	return r.Pinned(ctx, projectRoot, t.pin)
}

// pinnedRunLine is the command that runs args under the forge the project
// pins, for a message to hand back. The project's own .tools/bin/forge is
// named when it reports the pin (nothing to download or build); otherwise the
// `go run` form, which fetches and builds the pin into Go's cache.
func pinnedRunLine(projectRoot, pin string, args []string) string {
	if local, ok := pinDelegateResolver(io.Discard).LocalPinned(context.Background(), projectRoot, pin); ok {
		if abs, err := filepath.Abs(local); err == nil {
			local = abs
		}
		return forgecompat.RunBinaryCommand(local, args)
	}
	return forgecompat.RunPinnedCommand(pin, args)
}

// serveRequestsUnderTheirOwnPins prepares a process that serves many
// commands, each for its own project (forge mcp): no request may replace the
// process with another forge, and none inherits the start-up's verdict — were
// this process itself a delegate, every request would skip its pin check and
// run against a project pinned to a different forge. Each request's command
// is then checked against its own project by the root's PersistentPreRun,
// and refused or warned about there.
func serveRequestsUnderTheirOwnPins() {
	pinDelegationDisabled = true
	isDelegate = func() bool { return false }
}
