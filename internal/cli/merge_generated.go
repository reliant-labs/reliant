package cli

// A git merge driver for forge-generated files.
//
// WHY. Some generated files carry a line whose value is a fingerprint of
// OTHER files: the `forge:hash=` header every generated file starts with, the
// mock fixtures' SEED_FINGERPRINT (a digest of every migration). Any two
// branches that each change a migration or a generated file's body change
// that ONE line differently, so git reports a conflict there even when the
// real changes are nowhere near each other. On 2026-10-07 these were 2 of the
// 4 real conflicts in control-plane's stale checkout, and every forge pin PR
// conflicted with every other open branch.
//
// The fingerprint lines are not content anyone resolves by hand: they are
// recomputed by the next `forge generate`, and forge's own freshness checks
// (and CI's verify-generated) fail until that happens. So the driver merges
// everything ELSE with git's normal three-way merge, and takes the fingerprint
// lines from the current side. A real conflict in the body is still a
// conflict; a merge that touched only fingerprints is clean, and the stale
// fingerprint is exactly what `forge generate` refreshes.
//
// The proto descriptor (gen/forge_descriptor/) never reaches the driver: it is
// derived on demand and never committed (codegen.LoadDescriptor).
//
// Beyond fingerprints, the driver settles the two kinds of file every branch
// that adds a service changes (P3b friction F16, 33 parallel lanes):
//   - forge's committed state merges key by key (checksums.MergeStateFile):
//     disowned.json, and a scaffold birth or JSON certificate, each its own
//     file under .forge/scaffolded/ or .forge/hashes/, so two lanes' births
//     never even meet. A JSON output's certificate is its own file under
//     .forge/hashes/, which conflicts only when both sides re-rendered that
//     output; the driver then keeps this side's, with this side's render;
//   - a forge render both sides changed whose line merge still conflicts —
//     the service mount list — is not content a person resolves either.
//     When both sides are forge's own renders, the driver keeps this side's
//     (still a verifiable render) and the next `forge generate` renders the
//     merged inputs over it. A side carrying a hand edit keeps the conflict,
//     so nothing human is dropped. Outputs that need no such fallback are
//     written one file per service instead (the observability spec, its
//     saved searches and alert inputs: obsdash.ServicePart), which merges
//     into the render itself.
//
// Wiring needs nothing committed, and touches only the project's own
// repository (the project directory is the top of its work tree):
//   - every forge command that writes generated or state files (generate,
//     scaffold, disown, …: cmdutil.ConfiguresClone), and not on a --dry-run,
//     writes the driver's command into the repository's config
//     (ensureGeneratedMergeDriver) when it has none that works. Read-only and
//     slot commands never write there. One command serves every worktree,
//     whatever each pins: the driver hands each merge to the forge the
//     merged file's project pins (merge_driver_pin.go).
//   - the attributes that route forge's files to the driver are a committed
//     .gitattributes in a scaffolded project. A project adopted in place,
//     which never got forge's .gitattributes, gets the same routes in the
//     clone's info/attributes (shared by its worktrees, and reported).
//     FORGE_NO_MERGE_DRIVER=1 opts out of the driver; FORGE_NO_GIT_HOOKS=1
//     (or FORGE_NO_HOOKS) is about hooks only and leaves the driver alone.
//   - `forge merge-driver install` wires a checkout on purpose, even under
//     FORGE_NO_MERGE_DRIVER (a merge queue's pool), and `forge merge-driver
//     check` fails a checkout whose driver cannot run (merge_driver_install.go).
// A clone where forge has not run yet gets git's built-in merge, which is the
// old behaviour — never worse.
//
// A driver never reports a merge forge did not do. The binary a command names
// can vanish (pruned copies, removed tool caches): the command then
// reinstalls that same forge version and merges with it, and when it cannot,
// the file is left conflicted, with git's markers, and the driver says which
// forge is gone and how to repair it (driverCommandLine).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// generatedMergeDriver is the driver's name in .gitattributes and .git/config.
const generatedMergeDriver = "forge-generated"

// volatileLine matches a generated line whose value is a fingerprint of other
// files. Group 1 is the line's KEY: two lines with the same key are the same
// fact, whichever value they carry.
var volatileLine = regexp.MustCompile(`^(\s*(?://|#|<!--|/?\*)?\s*forge:hash=|export const SEED_FINGERPRINT\s*=|export const SEED_CONFIG_FINGERPRINT\s*=|export const SEED_FINGERPRINT_FILES\s*=)`)

// volatilePlaceholder replaces a fingerprint's value while the rest merges.
const volatilePlaceholder = "__FORGE_MERGE_GENERATED_VOLATILE__"

// neutralize replaces every fingerprint line's value with the placeholder and
// returns the original lines by key.
func neutralize(text []byte) ([]byte, map[string]string) {
	lines := strings.SplitAfter(string(text), "\n")
	originals := map[string]string{}
	for i, line := range lines {
		m := volatileLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		if _, seen := originals[key]; !seen {
			originals[key] = line
		}
		nl := ""
		if strings.HasSuffix(line, "\n") {
			nl = "\n"
		}
		lines[i] = key + volatilePlaceholder + nl
	}
	return []byte(strings.Join(lines, "")), originals
}

// restore puts fingerprint lines back: ours where ours has the key, else
// theirs. A placeholder line whose key neither side has cannot happen (the
// placeholder only exists for keys a side had), but is kept verbatim if it does.
func restore(merged []byte, ours, theirs map[string]string) []byte {
	lines := strings.SplitAfter(string(merged), "\n")
	for i, line := range lines {
		j := strings.Index(line, volatilePlaceholder)
		if j < 0 {
			continue
		}
		key := line[:j]
		switch {
		case ours[key] != "":
			lines[i] = ours[key]
		case theirs[key] != "":
			lines[i] = theirs[key]
		}
	}
	return []byte(strings.Join(lines, ""))
}

// mergeGenerated three-way merges a generated file with its fingerprint lines
// taken out of the comparison. clean is false when the rest of the file
// conflicts; merged then carries git's conflict markers.
func mergeGenerated(ctx context.Context, base, ours, theirs []byte, markerSize int) (merged []byte, clean bool, err error) {
	nBase, _ := neutralize(base)
	nOurs, ourLines := neutralize(ours)
	nTheirs, theirLines := neutralize(theirs)

	dir, err := os.MkdirTemp("", "forge-merge-generated-")
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	paths := map[string][]byte{"ours": nOurs, "base": nBase, "theirs": nTheirs}
	for name, body := range paths {
		if err := atomicfile.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			return nil, false, err
		}
	}
	if markerSize <= 0 {
		markerSize = 7
	}
	cmd := exec.CommandContext(ctx, "git", "merge-file", "-p", "--marker-size="+strconv.Itoa(markerSize),
		"-L", "ours", "-L", "base", "-L", "theirs",
		filepath.Join(dir, "ours"), filepath.Join(dir, "base"), filepath.Join(dir, "theirs"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		clean = true
	case errors.As(runErr, &exitErr) && exitErr.ExitCode() > 0 && exitErr.ExitCode() < 128:
		// git merge-file exits with the number of conflicts.
		clean = false
	default:
		return nil, false, fmt.Errorf("git merge-file: %v: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	merged = restore(out, ourLines, theirLines)
	if clean && checksums.Verify(ours) == checksums.Pristine && checksums.Verify(theirs) == checksums.Pristine {
		merged = recertify(merged)
	}
	return merged, clean, nil
}

// recertify rewrites the forge:hash marker to certify the merged body.
//
// Ours' marker certifies ours' body, not the merge, so kept as is the merged
// file verifies Modified and the next `forge generate` refuses it as a
// hand-edit. A clean merge of two PRISTINE renders holds no human edit, so it
// is certified as a render of some vintage, which generate overwrites with
// the current render. mergeGenerated only calls this when both sides verify:
// a hand-edited side keeps ours' marker, so its edit stays Modified and the
// stomp guard still asks before anything discards it.
func recertify(merged []byte) []byte {
	old, ok := checksums.ExtractMarker(merged)
	if !ok {
		return merged
	}
	key := "forge:hash="
	return bytes.Replace(merged, []byte(key+old), []byte(key+checksums.BodyHash(merged)), 1)
}

// mergeGeneratedPath merges one file git routed to the driver. relPath is
// git's %P (repository-relative), empty when an older driver command did not
// pass it. note, when set, says what the driver decided for the reader of
// the merge output.
func mergeGeneratedPath(ctx context.Context, relPath string, base, ours, theirs []byte, markerSize int) (merged []byte, clean bool, note string, err error) {
	if relPath != "" {
		if m, ok := checksums.MergeStateFile(relPath, base, ours, theirs); ok {
			return m, true, "", nil
		}
	}
	merged, clean, err = mergeGenerated(ctx, base, ours, theirs, markerSize)
	if err != nil {
		return nil, false, "", err
	}
	keep := "kept this side's render; run `forge generate` to render both sides' inputs into it"
	switch {
	case relPath != "" && !checksums.Stampable(relPath):
		// A comment-incapable output (JSON) is certified by
		// its certificate under .forge/hashes/, which keeps THIS side's hash
		// where both sides re-rendered it (MergeStateFile). A merged body — clean or not —
		// would match no recorded hash, and generate would refuse it as a
		// hand edit. So when this side is forge's verified render, keep it.
		if unstampableRenderOfOurs(relPath, ours) {
			return ours, true, keep, nil
		}
		// The service catalog is not settled here: it is disownable, and a
		// merge driver cannot see the other side's ownership record (git
		// runs it before MERGE_HEAD exists), so a line both sides changed
		// stays a conflict.
		// Its disjoint changes merge as lines, which is the common case.
	case !clean && checksums.Verify(ours) == checksums.Pristine && checksums.Verify(theirs) == checksums.Pristine:
		// Two pristine renders still conflicting: both are pure functions
		// of inputs that merged, so the conflict holds no human content.
		return ours, true, keep, nil
	}
	return merged, clean, "", nil
}

// unstampableRenderOfOurs reports whether ours is forge's render of a
// comment-incapable output: its body hash is the one THIS side's
// certificate (.forge/hashes/<path>.sha256, or a legacy .forge/hashes.json)
// records for it. The project root is the nearest directory above relPath
// holding forge.yaml (the driver runs at the top of the work tree, and
// relPath is relative to it); this side's certificate comes from HEAD, which
// is this side in a merge, a rebase and a cherry-pick alike.
func unstampableRenderOfOurs(relPath string, ours []byte) bool {
	rel, ok := projectRelative(relPath)
	if !ok {
		return false
	}
	prefix := strings.TrimSuffix(filepath.ToSlash(relPath), rel)
	if data, err := exec.CommandContext(context.Background(), "git", "cat-file", "blob", "HEAD:"+prefix+checksums.HashCertPath(rel)).Output(); err == nil {
		recorded, ok := checksums.ParseHashCert(data)
		return ok && recorded == checksums.BodyHash(ours)
	}
	data, err := exec.CommandContext(context.Background(), "git", "cat-file", "blob", "HEAD:"+prefix+checksums.LegacyHashesFile).Output()
	if err != nil {
		return false
	}
	var h struct {
		Files map[string]string `json:"files"`
	}
	if json.Unmarshal(data, &h) != nil {
		return false
	}
	recorded, ok := h.Files[rel]
	return ok && recorded == checksums.BodyHash(ours)
}

// projectRelative is relPath (relative to the top of the work tree, where git
// runs the driver) made relative to the forge project holding it: the nearest
// directory above it with a forge.yaml. ok is false outside any project.
func projectRelative(relPath string) (string, bool) {
	rel := filepath.ToSlash(relPath)
	for dir := path.Dir(rel); ; dir = path.Dir(dir) {
		prefix := ""
		if dir != "." {
			prefix = dir + "/"
		}
		if _, err := os.Stat(filepath.FromSlash(prefix + "forge.yaml")); err == nil {
			return strings.TrimPrefix(rel, prefix), true
		}
		if dir == "." || dir == "/" {
			return "", false
		}
	}
}

// newMergeGeneratedCmd is the driver git invokes:
//
//	forge merge-generated %O %A %B %L %P
//
// It writes the result into %A, as git's driver contract requires, and exits
// non-zero on a conflict. %P (the path being merged) is optional, so a
// driver command an older forge wrote keeps working.
func newMergeGeneratedCmd() *cobra.Command {
	return &cobra.Command{
		// git runs this mid-merge: no hook activation, no other side effects.
		Annotations: map[string]string{skipHookActivationAnnotation: ""},
		Use:         "merge-generated <base> <ours> <theirs> [marker-size] [path]",
		Short:       "git merge driver for forge-generated files (configured by forge; not run by hand)",
		Hidden:      true,
		Args:        cobra.RangeArgs(3, 5),
		// A merge driver's stdout/stderr land in the middle of git's own
		// output; nothing here should print a usage block.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			touchDriverCopy()
			base, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			ours, err := os.ReadFile(args[1])
			if err != nil {
				return err
			}
			theirs, err := os.ReadFile(args[2])
			if err != nil {
				return err
			}
			marker := 7
			if len(args) >= 4 {
				if n, perr := strconv.Atoi(args[3]); perr == nil {
					marker = n
				}
			}
			relPath := ""
			if len(args) == 5 {
				relPath = args[4]
			}
			// Only the forge the project pins merges by its rules; see
			// merge_driver_pin.go.
			if handled, clean, err := pinnedMerge(cmd.Context(), cmd.ErrOrStderr(), args, relPath, base, ours, theirs, marker); handled {
				if err != nil {
					return err
				}
				if !clean {
					return exitCodeError{code: 1, msg: "conflicts remain"}
				}
				return nil
			}
			merged, clean, note, err := mergeGeneratedPath(cmd.Context(), relPath, base, ours, theirs, marker)
			if err != nil {
				return err
			}
			if err := atomicfile.WriteFile(args[1], merged, 0o644); err != nil {
				return err
			}
			if note != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "forge merge-generated %s: %s\n", relPath, note)
			}
			if !clean {
				return exitCodeError{code: 1, msg: "conflicts outside forge's fingerprint lines remain"}
			}
			return nil
		},
	}
}

// ensureGeneratedMergeDriver configures the clone to merge forge's files
// through the driver: the driver command in the repository's config, and,
// when the project's committed .gitattributes does not already say so, the
// attributes routing its generated and state files to it in the clone's
// info/attributes. Both are shared by every linked worktree. Best-effort and
// idempotent; when it changes something it says what, where, and who shares
// it.
//
// Only the project's OWN repository is configured: the project directory must
// be the top of its git work tree. A project inside some other repository — a
// scratch directory under a checkout, `forge project new` run inside another
// repo — leaves that repository alone and says so in one line; forge does not
// rewrite the git config of a repository it was merely found inside. `forge
// merge-driver install` wires an enclosing repository on purpose (a monorepo
// that commits a forge project in a subdirectory); the routes it writes are
// then scoped to the project's directory.
//
// The attributes live in info/attributes, not .gitattributes, because they
// must reach every forge project: one adopted in place keeps its own
// .gitattributes (reliant had none), and forge does not rewrite a file the
// project owns.
//
// The command names this binary by absolute path. A bare name resolves on
// PATH at merge time, which is either nothing (a pinned forge run by path)
// or a different forge. An ephemeral binary (a test's build, anything under
// the temp directory) is never written, because it is gone by the next
// merge. A `go run` build is ephemeral too, but it is how a pin script runs
// the pinned forge (`go run .../cmd/forge@<pin> generate`), so the pinned
// one installs a durable copy of itself and names that (durableDriverCopy).
//
// ONE command serves every worktree, whatever each one pins, because the
// driver does not merge by its own rules: at merge time it hands the merge to
// the forge the merged file's project pins (pinnedMerge, merge_driver_pin.go).
// So the pinned forge records itself under its identity
// (forge-merge-driver.<identity>.command — one key per pin, so worktrees on
// different pins never contend for it), and the command itself is rewritten
// only when it cannot do that hand-off: it is missing, names a forge that is
// gone, or predates the hand-off (driverCommandDelegates). An earlier forge
// kept a command per worktree in config.worktree, which meant enabling
// extensions.worktreeConfig in the shared config of every clone it ran in; the
// hand-off made that unnecessary, so a per-worktree command an earlier forge
// wrote is removed here and the shared one applies.
//
// FORGE_NO_MERGE_DRIVER=1 is the one opt-out. FORGE_NO_GIT_HOOKS (and
// FORGE_NO_HOOKS) used to skip the driver as well, and that is how
// reliant's pin script — which sets FORGE_NO_HOOKS to leave core.hooksPath
// alone — left the driver naming the previous pin's forge: the driver is
// local git config, not a hook, and the hooks opt-out no longer touches it.
func ensureGeneratedMergeDriver(root string) {
	ensureGeneratedMergeDriverTo(os.Stderr, root)
}

// ensureGeneratedMergeDriverTo is ensureGeneratedMergeDriver with the notice
// written to w.
func ensureGeneratedMergeDriverTo(w io.Writer, root string) {
	if os.Getenv(mergeDriverOptOutEnv) != "" {
		return
	}
	wireGeneratedMergeDriver(w, root, false)
}

// wireGeneratedMergeDriver is ensureGeneratedMergeDriverTo without the
// FORGE_NO_MERGE_DRIVER opt-out. enclosing permits a repository whose work
// tree merely contains the project: `forge merge-driver install`, which a
// script that otherwise opts out runs on purpose, passes true.
func wireGeneratedMergeDriver(w io.Writer, root string, enclosing bool) {
	top := gitTrimmed(root, "rev-parse", "--show-toplevel")
	if top == "" {
		return // not in a git work tree
	}
	if !enclosing && !sameDirectory(top, root) {
		fmt.Fprintf(w, "forge: left the git repository at %s alone: it is not this project's own (%s is not its top level), so forge's merge driver is not wired there; `%s merge-driver install` wires it on purpose.\n",
			top, root, cmdutil.Name())
		return
	}
	var changes []string
	wroteAttributes := false
	// The routes are harmless without a command (git merges a file whose
	// driver is undefined as text), so they are written even when this
	// binary cannot be the command. When the project's committed
	// .gitattributes already routes the files, nothing is written: the
	// declaration travels with the repository and needs no per-clone copy.
	if !committedRoutesCover(root) {
		if file, ok := writeMergeAttributes(root); ok {
			wroteAttributes = true
			changes = append(changes, fmt.Sprintf("%s: routes forge's generated and state files to the driver", file))
		}
	}
	key := "merge." + generatedMergeDriver + ".driver"
	nameKey := "merge." + generatedMergeDriver + ".name"
	// A per-worktree command an earlier forge wrote (config.worktree) would
	// shadow the shared one in this worktree forever; it is forge's own
	// state, so it goes. Only read with the extension on: without it,
	// `--worktree` means the shared config.
	if gitTrimmed(root, "config", "--local", "--type=bool", "--get", "extensions.worktreeConfig") == "true" {
		if old := gitTrimmed(root, "config", "--worktree", "--get", key); old != "" &&
			exec.CommandContext(context.Background(), "git", "-C", root, "config", "--worktree", "--unset", key).Run() == nil {
			_ = exec.CommandContext(context.Background(), "git", "-C", root, "config", "--worktree", "--unset", nameKey).Run()
			changes = append(changes, fmt.Sprintf("removed this worktree's own %s (%s): the shared command applies, and hands each merge to the forge the project pins", key, old))
		}
	}
	current := gitTrimmed(root, "config", "--get", key)
	// A forge that is not the project's pin never installs itself, and an
	// unusable command is replaced by whatever forge runs. The pinned forge
	// also replaces a command that cannot hand a merge to it — that is what
	// repairs a driver from before the hand-off. Only the pinned forge
	// copies a `go run` build of itself somewhere durable.
	_, _, skewed := mergeDriverPinCheck(root)
	var command string
	var ok bool
	if registered, have := pinnedDriverCommand(root, mergeDriverIdentity()); have && !skewed {
		// A binary of this same pinned identity already registered itself
		// and is still on disk: it IS the pinned forge, so keep it. Without
		// this, every copy of the pin at a different path — each worktree's
		// tool-cache install, a `go install`ed one, a durable `go run` copy
		// — rewrote the config to name itself, and two of them alternating
		// rewrote it on every run.
		command, ok = registered, true
	} else {
		command, ok = mergeDriverCommand(!skewed)
	}
	if ok && !skewed && registerPinnedDriver(root, command) {
		changes = append(changes, fmt.Sprintf("%s.%s.command = %s: the driver of this forge's identity, for a driver of another pin to hand a merge to",
			pinnedDriverSection, mergeDriverIdentity(), command))
	}
	if ok && current != command && (!driverCommandUsable(current) || (!skewed && !driverCommandDelegates(current))) {
		_ = exec.CommandContext(context.Background(), "git", "-C", root, "config", "--local",
			nameKey, "forge-generated files: merge, keeping fingerprint lines for forge generate to refresh").Run()
		if exec.CommandContext(context.Background(), "git", "-C", root, "config", "--local", key, command).Run() == nil {
			was := "unset"
			if current != "" {
				was = current
			}
			changes = append(changes, fmt.Sprintf("%s = %s (was: %s)", key, command, was))
		}
	}
	if len(changes) > 0 {
		_, _ = fmt.Fprint(w, mergeDriverNotice(root, changes, wroteAttributes))
	}
}

// driverCommandDelegates reports whether a configured driver command hands
// each merge to the forge the project pins. Every command forge has written
// since the hand-off (#785) opens with the `[ -x <bin> ]` guard
// (driverCommandLine, #1203), so the guard is the tell.
func driverCommandDelegates(command string) bool {
	return strings.HasPrefix(command, driverGuardPrefix) && driverCommandUsable(command)
}

// sameDirectory reports whether a and b name the same directory, through
// symlinks (macOS's /var is /private/var; git prints the resolved form).
func sameDirectory(a, b string) bool {
	ra, aerr := filepath.EvalSymlinks(a)
	rb, berr := filepath.EvalSymlinks(b)
	if aerr != nil || berr != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	if ra == rb {
		return true
	}
	ia, aerr := os.Stat(ra)
	ib, berr := os.Stat(rb)
	return aerr == nil && berr == nil && os.SameFile(ia, ib)
}

// committedRoutesCover reports whether the project's committed .gitattributes
// already routes every file forge's block would. Then the declaration travels
// with the repository (a scaffolded project has it), and writing the same
// routes into the shared info/attributes would only be a second copy that
// every worktree carries.
func committedRoutesCover(root string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil {
		return false
	}
	routed := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && !strings.HasPrefix(f[0], "#") {
			for _, attr := range f[1:] {
				if attr == "merge="+generatedMergeDriver {
					routed[f[0]] = true
				}
			}
		}
	}
	for _, p := range generatedMergePatterns {
		if !routed[p] {
			return false
		}
	}
	return true
}

// mergeDriverNotice says what ensureGeneratedMergeDriver changed, where, and
// who else it affects. The scope is the point: .git/config and
// info/attributes live in the repository's COMMON git directory, which every
// linked worktree reads, so a change made from one worktree applies to all of
// them — and to every other agent working in them.
func mergeDriverNotice(root string, changes []string, wroteAttributes bool) string {
	common := gitTrimmed(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	var b strings.Builder
	b.WriteString("forge: set up merging of forge's generated and state files through `forge merge-generated`")
	if common != "" {
		fmt.Fprintf(&b, " (git directory %s", common)
		if n := strings.Count(gitTrimmed(root, "worktree", "list", "--porcelain"), "worktree "); n > 1 {
			fmt.Fprintf(&b, ", shared by %d worktrees", n)
		}
		b.WriteString(")")
	}
	b.WriteString(". Changed, for every worktree of this repository:\n")
	for _, c := range changes {
		b.WriteString("  - " + c + "\n")
	}
	undo := fmt.Sprintf("`git config --unset merge.%s.driver`", generatedMergeDriver)
	if wroteAttributes {
		undo += " and delete the \"forge merge-generated\" block from info/attributes"
	}
	fmt.Fprintf(&b, "  Undo: %s. Never again: %s=1.\n", undo, mergeDriverOptOutEnv)
	return b.String()
}

// gitTrimmed is `git -C dir args...` trimmed, "" on any failure.
func gitTrimmed(dir string, args ...string) string {
	out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// mergeDriverOptOutEnv skips wiring the merge driver into a clone, leaving
// git hook activation alone.
const mergeDriverOptOutEnv = "FORGE_NO_MERGE_DRIVER"

// driverCommandUsable reports whether a configured driver command already
// does the job: it passes %P (so it is a forge that merges state files) and
// its binary still exists.
func driverCommandUsable(command string) bool {
	if !strings.Contains(command, " merge-generated %O %A %B %L %P") {
		return false
	}
	bin := strings.TrimPrefix(command, driverGuardPrefix)
	if strings.HasPrefix(bin, "'") {
		var b strings.Builder
		rest := bin[1:]
		for {
			end := strings.Index(rest, "'")
			if end < 0 {
				return false
			}
			b.WriteString(rest[:end])
			rest = rest[end+1:]
			if !strings.HasPrefix(rest, `\''`) {
				break
			}
			b.WriteByte('\'')
			rest = rest[3:]
		}
		bin = b.String()
	} else if i := strings.IndexByte(bin, ' '); i >= 0 {
		bin = bin[:i]
	}
	return isExecutableFile(bin)
}

// driverGuardPrefix opens every driver command forge writes: `[ -x '<bin>' ]`
// is the test that the named forge still exists (driverCommandLine).
const driverGuardPrefix = "[ -x "

// driverCommandLine is the command git runs through `sh -c` for one merge: the
// forge at bin when it still exists; else the same forge, reinstalled; else a
// merge that FAILS, loudly.
//
// The command names ONE binary by absolute path, and that binary can vanish —
// the durable copies are pruned to the most recently used identities
// (keptDriverCopies), a worktree on an older pin outlives its copy, and a
// removed tool cache takes an installed one. On 2026-10-26 thirteen of
// reliant's worktrees named an evicted copy.
//
// So when bin is gone and this forge has a module version, the command
// reinstalls that version into its durable-copy directory
// (`go install …/cmd/forge@<version>`, GOBIN=<cache>/<identity>) and merges
// with it: the same forge, so the same rules. When that is impossible (no
// version, no go, no network) the merge fails: git's text merge is written
// into %A, markers and all, the driver exits 1 so git reports the file
// conflicted, and it says which forge is gone and what repairs it. It never
// exits 0 without forge having merged: an earlier fallback kept this side of a
// conflicting generated file and exited 0, so git recorded a stale mock as
// cleanly merged and nothing downstream knew to regenerate it.
func driverCommandLine(bin, name string) string {
	q := shellQuote(bin)
	words := strings.Fields(name)
	words[0] = q
	return driverGuardPrefix + q + " ] && exec " + strings.Join(words, " ") + " merge-generated %O %A %B %L %P; " +
		driverReinstallClause() +
		"git merge-file --marker-size=%L -L ours -L base -L theirs %A %O %B; " +
		"printf 'forge merge-generated %s: FAILED: the forge this merge driver names (%s) is gone and could not be reinstalled, " +
		"so forge did not merge this file; it is left conflicted. Run `" + name + " merge-driver install` with the pinned forge, then merge again.\\n' %P " +
		q + " >&2; exit 1"
}

// driverReinstallClause is the part of the driver command that brings a
// vanished forge back: this binary's own module version, installed into its
// durable-copy directory, then run. "" when this forge has no version to
// install (a development build) or no durable-copy directory.
func driverReinstallClause() string {
	version := mergeDriverVersion()
	cache, err := mergeDriverCacheDir()
	if !installableForgeVersion(version) || err != nil {
		return ""
	}
	id := forgeIdentity(version)
	d := shellQuote(filepath.Join(cache, id))
	return "d=" + d + "; [ -x \"$d/forge\" ] || { printf 'forge merge-generated %s: the forge this driver names is gone; reinstalling forge " + version + " into %s\\n' %P \"$d\" >&2; " +
		"(cd / && GOWORK=off GOFLAGS= GOBIN=\"$d\" go install " + forgeModulePath + "/cmd/forge@" + version + " >&2); }; " +
		"[ -x \"$d/forge\" ] && exec \"$d/forge\" merge-generated %O %A %B %L %P; "
}

// installableForgeVersion reports whether the module proxy can serve forge
// at v: a release tag or a pseudo-version, with no build metadata (a `+dirty`
// or `+unknown` build names no commit the proxy has).
func installableForgeVersion(v string) bool {
	return installableVersionRE.MatchString(v) && !strings.Contains(v, ".dev")
}

var installableVersionRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// ephemeralExecutable reports whether exe will be gone by the next merge: a
// `go test` binary, a `go run` build, or anything under the temp directory.
func ephemeralExecutable(exe string) bool {
	if strings.HasSuffix(exe, ".test") || goRunExecutable(exe) {
		return true
	}
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		tmp = os.TempDir()
	}
	rel, err := filepath.Rel(tmp, exe)
	return err == nil && !strings.HasPrefix(rel, "..")
}

// goRunExecutable reports whether exe is a `go run` build. go run links
// into its work directory (<tmp>/go-build<N>/b001/exe/<name>), deleted when
// it exits, or — since go 1.24 — into the build cache's executable store
// (<GOCACHE>/<2 hex>/<64 hex>-d/<name>), which `go clean -cache` and cache
// trimming delete. The shape is matched rather than GOCACHE's location,
// which `go env -w` can move without the environment saying so.
func goRunExecutable(exe string) bool {
	if strings.HasSuffix(exe, ".test") {
		return false
	}
	parts := strings.Split(filepath.ToSlash(exe), "/")
	for _, p := range parts {
		if strings.HasPrefix(p, "go-build") {
			return true
		}
	}
	n := len(parts)
	return n >= 3 && goCacheExeDirRE.MatchString(parts[n-2]) && goCacheShardRE.MatchString(parts[n-3])
}

var (
	goCacheExeDirRE = regexp.MustCompile(`^[0-9a-f]{64}-d$`)
	goCacheShardRE  = regexp.MustCompile(`^[0-9a-f]{2}$`)
)

// mergeDriverCommand is the driver command line for this binary: its
// absolute path plus the route that reaches merge-generated (`forge`, or
// `reliant forge` when forge is mounted as a subcommand). ok is false for an
// ephemeral binary, which must never become a repository's merge driver —
// except a `go run` build of the pinned forge (pinned), which names a
// durable copy of itself instead (durableDriverCopy).
func mergeDriverCommand(pinned bool) (string, bool) {
	exe, err := mergeDriverExecutable()
	if err != nil {
		return "", false
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	if ephemeralExecutable(exe) {
		if !pinned || !goRunExecutable(exe) {
			return "", false
		}
		durable, derr := durableDriverCopy(exe)
		if derr != nil {
			return "", false
		}
		exe = durable
	}
	return driverCommandLine(exe, cmdutil.Name()), true
}

// mergeDriverCacheDir is where durable copies of `go run` forges live, one
// directory per forge identity. A seam for tests.
var mergeDriverCacheDir = func() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "forge", "merge-driver"), nil
}

// keptDriverCopies is how many forge identities keep a durable copy. A copy
// is the whole binary, and a pin-per-fix workflow makes several a day. A
// clone whose driver named an evicted copy has an unusable driver: the next
// forge command repairs it, and a merge before that falls back to git's
// plain merge, saying why (merge_driver_pin.go).
const keptDriverCopies = 3

// durableDriverCopy returns a path that will outlive this process holding
// exe's bytes: <cache>/<identity>/<name>, written once per identity (a hard
// link where the filesystem allows it, else a copy) and reused after that.
//
// reliant pins forge with `go run .../cmd/forge@<pin> generate`, the one
// command that runs the pinned forge without installing it, and #785's
// repair is "the pinned forge points the driver at itself". Refusing the
// ephemeral path left the driver naming the previous pin's forge until
// someone happened to run the new one from a real path.
func durableDriverCopy(exe string) (string, error) {
	id := mergeDriverIdentity()
	if id == "" {
		return "", errors.New("this forge has no identity to file a copy under")
	}
	cache, err := mergeDriverCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, id)
	dst := filepath.Join(dir, filepath.Base(exe))
	src, err := os.Stat(exe)
	if err != nil {
		return "", err
	}
	now := time.Now()
	if have, err := os.Stat(dst); err == nil && have.Size() == src.Size() && have.Mode()&0o111 != 0 {
		_ = os.Chtimes(dir, now, now) // most recently used: kept by pruning
		return dst, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(exe)+".tmp-"+strconv.Itoa(os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Link(exe, tmp); err != nil {
		if err := copyExecutable(exe, tmp); err != nil {
			_ = os.Remove(tmp)
			return "", err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	_ = os.Chtimes(dir, now, now)
	pruneDriverCopies(cache, id)
	return dst, nil
}

// copyExecutable copies src to dst with an executable mode.
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// pruneDriverCopies keeps the keptDriverCopies most recently used identity
// directories under cache, and always the one named keep.
func pruneDriverCopies(cache, keep string) {
	entries, err := os.ReadDir(cache)
	if err != nil {
		return
	}
	type copyDir struct {
		name string
		used time.Time
	}
	var dirs []copyDir
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keep {
			continue
		}
		if info, err := e.Info(); err == nil {
			dirs = append(dirs, copyDir{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].used.After(dirs[j].used) })
	for i := keptDriverCopies - 1; i < len(dirs); i++ {
		_ = os.RemoveAll(filepath.Join(cache, dirs[i].name))
	}
}

// mergeDriverExecutable is os.Executable, a seam for tests: an ephemeral
// binary is refused as a driver, so a test that exercises the wiring names
// the binary it means.
var mergeDriverExecutable = os.Executable

// mergeAttributesBegin and mergeAttributesEnd bracket forge's block in
// info/attributes, so it is replaced in place and nothing else is touched.
const (
	mergeAttributesBegin = "# >>> forge merge-generated (managed by forge; FORGE_NO_MERGE_DRIVER=1 stops it)"
	mergeAttributesEnd   = "# <<< forge merge-generated"
)

// generatedMergePatterns are the files routed to the driver, relative to
// the project root: forge's generated outputs and its committed state.
var generatedMergePatterns = []string{
	"*_gen.go", "*_gen.ts", "*_gen.test.ts", "*_gen.json", "*_gen.ndjson",
	"forge_service_catalog.json",
	".forge/scaffolded/**", ".forge/hashes/**", ".forge/disowned.json",
}

// writeMergeAttributes writes forge's block into the clone's
// info/attributes, scoped to root's directory within the repository.
// Returns the file and whether it changed.
func writeMergeAttributes(root string) (string, bool) {
	attrPath, err := exec.CommandContext(context.Background(), "git", "-C", root, "rev-parse", "--path-format=absolute", "--git-path", "info/attributes").Output()
	if err != nil {
		return "", false
	}
	prefix, err := exec.CommandContext(context.Background(), "git", "-C", root, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return "", false
	}
	block := mergeAttributesBlock(strings.TrimSpace(string(prefix)))
	file := strings.TrimSpace(string(attrPath))
	existing, _ := os.ReadFile(file)
	updated := replaceManagedBlock(string(existing), block)
	if updated == string(existing) {
		return file, false
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return "", false
	}
	return file, atomicfile.WriteFile(file, []byte(updated), 0o644) == nil
}

// mergeAttributesBlock renders the block for a project at prefix (git's
// --show-prefix: "" at the repository root, else "dir/sub/").
func mergeAttributesBlock(prefix string) string {
	var b strings.Builder
	b.WriteString(mergeAttributesBegin + "\n")
	for _, p := range generatedMergePatterns {
		switch {
		case prefix == "":
			// A pattern with no slash matches at any depth; one with a
			// slash is anchored to the repository root.
			b.WriteString(p)
		case strings.Contains(p, "/"):
			b.WriteString(prefix + p)
		default:
			b.WriteString(prefix + "**/" + p)
		}
		b.WriteString(" merge=" + generatedMergeDriver + "\n")
	}
	b.WriteString(mergeAttributesEnd + "\n")
	return b.String()
}

// replaceManagedBlock swaps forge's block in content for block, appending
// it when there is none.
func replaceManagedBlock(content, block string) string {
	start := strings.Index(content, mergeAttributesBegin)
	if start >= 0 {
		if end := strings.Index(content[start:], mergeAttributesEnd); end >= 0 {
			end += start + len(mergeAttributesEnd)
			if end < len(content) && content[end] == '\n' {
				end++
			}
			return content[:start] + block + content[end:]
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + block
}
