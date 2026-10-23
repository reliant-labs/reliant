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
// gen/forge_descriptor.json's stamp is the exception that needs no refresh:
// it is one entry per .proto (codegen.DescriptorSources), so the driver
// merges it key by key and the result IS the stamp of the merged tree
// (mergeDescriptorSources).
//
// Beyond fingerprints, the driver settles the two kinds of file every branch
// that adds a service changes (P3b friction F16, 33 parallel lanes):
//   - forge's committed state files (.forge/scaffolded.json, hashes.json,
//     disowned.json) merge key by key (checksums.MergeStateFile), so two
//     lanes' births and hashes never conflict;
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
// Wiring is per worktree and needs nothing committed:
//   - every forge command that writes generated or state files (generate,
//     scaffold, disown, …: cmdutil.ConfiguresClone) (re)writes the driver's
//     command into that worktree's own config (ensureGeneratedMergeDriver),
//     and only when it differs. Read-only and slot commands never write
//     there. The command names ONE forge binary, and a repository's worktrees
//     are on different branches with different forge pins, so it is not a
//     shared fact: in the shared .git/config the last forge to run decided
//     the driver for every other worktree. It goes in config.worktree, which
//     needs extensions.worktreeConfig; forge enables that once and says so.
//   - the attributes that route forge's files to the driver are a committed
//     .gitattributes in a scaffolded project. A project adopted in place,
//     which never got forge's .gitattributes, gets the same routes in the
//     clone's info/attributes (shared by its worktrees, and reported).
//     FORGE_NO_MERGE_DRIVER=1 opts out of the driver; FORGE_NO_GIT_HOOKS=1
//     (or FORGE_NO_HOOKS) is about hooks only and leaves the driver alone.
//     FORGE_MERGE_DRIVER_SHARED=1 keeps the command in the shared config.
// A clone where forge has not run yet gets git's built-in merge, which is the
// old behaviour — never worse.

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
	// The descriptor's per-proto stamp merges key by key, outside the line
	// merge (see mergeDescriptorSources); the rest of the file merges below.
	if rOurs, ourSources, ok := splitDescriptorSources(ours); ok {
		if rTheirs, theirSources, ok := splitDescriptorSources(theirs); ok {
			rBase, baseSources, _ := splitDescriptorSources(base)
			rest, clean, err := mergeGenerated(ctx, rBase, rOurs, rTheirs, markerSize)
			if err != nil {
				return nil, false, err
			}
			return joinDescriptorSources(rest, mergeDescriptorSources(baseSources, ourSources, theirSources)), clean, nil
		}
	}

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

// descriptorSourcesKey opens the per-proto stamp block of
// gen/forge_descriptor.json, as json.MarshalIndent writes it (two-space
// indent; ForgeDescriptor.Sources).
const descriptorSourcesKey = `  "sources": `

// descriptorSourcesPlaceholder stands in for the stamp block while the rest
// of the descriptor goes through the line merge.
const descriptorSourcesPlaceholder = "__FORGE_MERGE_DESCRIPTOR_SOURCES__"

// splitDescriptorSources lifts the per-proto stamp block out of a forge
// descriptor, leaving a placeholder line, and returns the stamp. ok is false
// for any file without the block, which then merges as before.
func splitDescriptorSources(text []byte) (rest []byte, sources map[string]string, ok bool) {
	lines := strings.SplitAfter(string(text), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, descriptorSourcesKey) {
			continue
		}
		value := strings.TrimPrefix(line, descriptorSourcesKey)
		end := i
		if strings.HasPrefix(value, "{") && !strings.HasPrefix(strings.TrimSpace(value), "{}") {
			for end = i + 1; end < len(lines) && !strings.HasPrefix(lines[end], "  }"); end++ {
			}
			if end == len(lines) {
				return text, nil, false
			}
		}
		block := strings.TrimPrefix(strings.Join(lines[i:end+1], ""), descriptorSourcesKey)
		trimmed := strings.TrimRight(block, "\n")
		suffix := ""
		if strings.HasSuffix(trimmed, ",") {
			trimmed, suffix = strings.TrimSuffix(trimmed, ","), ","
		}
		if err := json.Unmarshal([]byte(trimmed), &sources); err != nil || sources == nil {
			return text, nil, false
		}
		placeholder := descriptorSourcesKey + descriptorSourcesPlaceholder + suffix + block[len(strings.TrimRight(block, "\n")):]
		out := strings.Join(lines[:i], "") + placeholder + strings.Join(lines[end+1:], "")
		return []byte(out), sources, true
	}
	return text, nil, false
}

// joinDescriptorSources renders sources where splitDescriptorSources left its
// placeholder, exactly as forge writes the field.
func joinDescriptorSources(merged []byte, sources map[string]string) []byte {
	if sources == nil {
		sources = map[string]string{}
	}
	rendered, err := json.MarshalIndent(sources, "  ", "  ")
	if err != nil {
		return merged
	}
	return bytes.Replace(merged, []byte(descriptorSourcesPlaceholder), rendered, 1)
}

// mergeDescriptorSources three-way merges the descriptor's per-proto stamp
// key by key. Each entry stamps one proto, and a side that left an entry as
// the base had it made no claim about that proto, so the other side's entry
// (an edit, an addition or a deletion) stands. This is what git's line merge
// cannot do: two branches that edit protos whose entries sit on neighbouring
// lines would conflict, though each entry is the truth about its own file.
//
// When both sides changed one entry differently, the proto itself was edited
// on both sides and its merge matches neither stamp: ours is kept, the
// descriptor reads as stale, and the next `forge generate` restamps it — the
// policy every other fingerprint line here follows.
func mergeDescriptorSources(base, ours, theirs map[string]string) map[string]string {
	out := map[string]string{}
	keys := map[string]bool{}
	for _, m := range []map[string]string{base, ours, theirs} {
		for k := range m {
			keys[k] = true
		}
	}
	for k := range keys {
		b, inBase := base[k]
		o, inOurs := ours[k]
		t, inTheirs := theirs[k]
		if inOurs == inBase && o == b {
			if inTheirs {
				out[k] = t
			}
			continue
		}
		if inOurs {
			out[k] = o
		}
	}
	return out
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
		// .forge/hashes.json, which keeps THIS side's hash where both sides
		// re-rendered (MergeStateFile). A merged body — clean or not —
		// would match no recorded hash, and generate would refuse it as a
		// hand edit. So when this side is forge's verified render, keep it.
		if unstampableRenderOfOurs(relPath, ours) {
			return ours, true, keep, nil
		}
		// The descriptor is rewritten whole on every generate and certified
		// by nothing: a clean merge stands, a conflict takes this side.
		if path.Base(relPath) == "forge_descriptor.json" && !clean {
			return ours, true, keep, nil
		}
	case !clean && checksums.Verify(ours) == checksums.Pristine && checksums.Verify(theirs) == checksums.Pristine:
		// Two pristine renders still conflicting: both are pure functions
		// of inputs that merged, so the conflict holds no human content.
		return ours, true, keep, nil
	}
	return merged, clean, "", nil
}

// unstampableRenderOfOurs reports whether ours is forge's render of a
// comment-incapable output: its body hash is the one THIS side's
// .forge/hashes.json records for it. The project root is the nearest
// directory above relPath holding forge.yaml (the driver runs at the top of
// the work tree, and relPath is relative to it); this side's hashes come
// from HEAD, which is this side in a merge, a rebase and a cherry-pick alike.
func unstampableRenderOfOurs(relPath string, ours []byte) bool {
	rel := filepath.ToSlash(relPath)
	for dir := path.Dir(rel); ; dir = path.Dir(dir) {
		prefix := ""
		if dir != "." {
			prefix = dir + "/"
		}
		if _, err := os.Stat(filepath.FromSlash(prefix + "forge.yaml")); err == nil {
			data, err := exec.CommandContext(context.Background(), "git", "cat-file", "blob", "HEAD:"+prefix+checksums.HashesFile).Output()
			if err != nil {
				return false
			}
			var h struct {
				Files map[string]string `json:"files"`
			}
			if json.Unmarshal(data, &h) != nil {
				return false
			}
			recorded, ok := h.Files[strings.TrimPrefix(rel, prefix)]
			return ok && recorded == checksums.BodyHash(ours)
		}
		if dir == "." || dir == "/" {
			return false
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
// through the driver: the driver command in this worktree's config, and, when
// the project's committed .gitattributes does not already say so, the
// attributes routing its generated and state files to it in the clone's
// info/attributes (shared by every linked worktree). Best-effort and
// idempotent; when it changes something it says what, where, and who shares it.
//
// The attributes live in info/attributes, not .gitattributes, because they
// must reach every forge project: one adopted in place keeps its own
// .gitattributes (reliant had none), and forge does not rewrite a file the
// project owns. They are scoped to the project's directory, so a forge
// project inside a larger repository routes only its own files.
//
// The command names this binary by absolute path. A bare name resolves on
// PATH at merge time, which is either nothing (a pinned forge run by path)
// or a different forge. An ephemeral binary (a test's build, anything under
// the temp directory) is never written, because it is gone by the next
// merge. A `go run` build is ephemeral too, but it is how a pin script runs
// the pinned forge (`go run .../cmd/forge@<pin> generate`), so the pinned
// one installs a durable copy of itself and names that (durableDriverCopy).
// The forge the project pins writes itself over any other driver, and
// records itself under its identity so a driver from another pin can hand it
// the merge; a forge that is NOT the pin only fills in a missing or unusable
// command (merge_driver_pin.go).
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
	// Any git work tree, including one where the project sits in a
	// subdirectory of the repository.
	if exec.CommandContext(context.Background(), "git", "-C", root, "rev-parse", "--is-inside-work-tree").Run() != nil {
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
			changes = append(changes, fmt.Sprintf("%s (shared): routes forge's generated and state files to the driver", file))
		}
	}
	key := "merge." + generatedMergeDriver + ".driver"
	cur, _ := exec.CommandContext(context.Background(), "git", "-C", root, "config", "--get", key).Output()
	current := strings.TrimSpace(string(cur))
	// A forge that is not the project's pin never installs itself, and an
	// unusable command is replaced by whatever forge runs. The pinned forge
	// takes the driver over from any other (merge_driver_pin.go): that is
	// what repairs a driver a pin bump left behind. Only the pinned forge
	// copies a `go run` build of itself somewhere durable.
	_, _, skewed := mergeDriverPinCheck(root)
	var scope configScope
	scoped := false
	needScope := func() configScope {
		if !scoped {
			var enabled bool
			scope, enabled = driverConfigScope(root)
			scoped = true
			if enabled {
				changes = append(changes, "extensions.worktreeConfig = true (shared): lets each worktree keep its own driver command")
			}
		}
		return scope
	}
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
	if ok && !skewed {
		if sc := needScope(); registerPinnedDriver(root, command, sc) {
			changes = append(changes, fmt.Sprintf("%s.%s.command = %s (%s): the driver of this forge's identity, for a driver of another pin to hand a merge to",
				pinnedDriverSection, mergeDriverIdentity(), command, sc.describe()))
		}
	}
	if ok && current != command && (!driverCommandUsable(current) || !skewed) {
		sc := needScope()
		_ = exec.CommandContext(context.Background(), "git", "-C", root, "config", sc.flag(),
			"merge."+generatedMergeDriver+".name", "forge-generated files: merge, keeping fingerprint lines for forge generate to refresh").Run()
		if exec.CommandContext(context.Background(), "git", "-C", root, "config", sc.flag(), key, command).Run() == nil {
			was := "unset"
			if current != "" {
				was = current
			}
			changes = append(changes, fmt.Sprintf("%s = %s (%s; was: %s)", key, command, sc.describe(), was))
		}
	}
	if len(changes) > 0 {
		_, _ = fmt.Fprint(w, mergeDriverNotice(root, changes, wroteAttributes))
	}
}

// mergeDriverSharedEnv keeps the driver command in the repository's shared
// config, as forge always did, for tooling that cannot cope with
// extensions.worktreeConfig.
const mergeDriverSharedEnv = "FORGE_MERGE_DRIVER_SHARED"

// configScope is where the driver command is written.
type configScope int

const (
	// scopeWorktree is this worktree's config.worktree: what only this
	// checkout merges with. Needs extensions.worktreeConfig.
	scopeWorktree configScope = iota
	// scopeShared is the repository's .git/config, read by every linked
	// worktree.
	scopeShared
)

func (s configScope) flag() string {
	if s == scopeWorktree {
		return "--worktree"
	}
	return "--local"
}

func (s configScope) describe() string {
	if s == scopeWorktree {
		return "this worktree only"
	}
	return "shared"
}

// driverConfigScope picks where the driver command is written, and reports
// whether it had to enable extensions.worktreeConfig to get there.
//
// The command names ONE forge binary by absolute path, and a repository's
// worktrees are routinely on different branches with different forge pins.
// In the shared config the last forge to run decides the driver for every
// worktree, and an agent's command in one checkout changes how another
// agent's rebase merges. In config.worktree each checkout has its own, so the
// forge a worktree's own commands run is the forge that merges there.
//
// The extension is the one shared change this needs, made once and reported.
// git has honoured it since 2.20 and go-git accepts it at repository format 0;
// it is not enabled where git documents a hazard (a bare repository or an
// explicit core.worktree, whose settings must then move to config.worktree),
// and FORGE_MERGE_DRIVER_SHARED=1 declines it.
func driverConfigScope(root string) (scope configScope, enabled bool) {
	if os.Getenv(mergeDriverSharedEnv) != "" {
		return scopeShared, false
	}
	if gitTrimmed(root, "config", "--local", "--type=bool", "--get", "extensions.worktreeConfig") == "true" {
		return scopeWorktree, false
	}
	if gitTrimmed(root, "config", "--local", "--type=bool", "--get", "core.bare") == "true" ||
		gitTrimmed(root, "config", "--local", "--get", "core.worktree") != "" {
		return scopeShared, false
	}
	if exec.CommandContext(context.Background(), "git", "-C", root, "config", "--local", "extensions.worktreeConfig", "true").Run() != nil {
		return scopeShared, false
	}
	// Prove the extension took before relying on it: a git too old to know
	// it rejects --worktree outright (exit 129), where reading a missing key
	// is a plain exit 1.
	if exitCode(exec.CommandContext(context.Background(), "git", "-C", root, "config", "--worktree", "--get", "forge.probe")) == 1 {
		return scopeWorktree, true
	}
	_ = exec.CommandContext(context.Background(), "git", "-C", root, "config", "--local", "--unset", "extensions.worktreeConfig").Run()
	return scopeShared, false
}

func exitCode(cmd *exec.Cmd) int {
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
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
	b.WriteString(". Changed:\n")
	for _, c := range changes {
		b.WriteString("  - " + c + "\n")
	}
	b.WriteString("  A setting marked \"this worktree only\" affects no other checkout; one marked \"shared\" affects all of them.\n")
	undo := fmt.Sprintf("`git config --worktree --unset merge.%s.driver` (without --worktree for a shared one)", generatedMergeDriver)
	if wroteAttributes {
		undo += " and delete the \"forge merge-generated\" block from info/attributes"
	}
	fmt.Fprintf(&b, "  Undo: %s. Never again: %s=1; keep the command in the shared config: %s=1.\n",
		undo, mergeDriverOptOutEnv, mergeDriverSharedEnv)
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
	info, err := os.Stat(bin)
	return err == nil && !info.IsDir()
}

// driverGuardPrefix opens every driver command forge writes: `[ -x '<bin>' ]`
// is the test that the named forge still exists (driverCommandLine).
const driverGuardPrefix = "[ -x "

// driverCommandLine is the command git runs through `sh -c` for one merge: the
// forge at bin when it still exists, and otherwise a merge that needs no forge.
//
// The command names ONE binary by absolute path, and that binary can vanish —
// the durable copies are pruned to the most recently used identities
// (keptDriverCopies), a worktree on an older pin outlives its copy, and a
// removed tool cache takes an installed one. A bare command then dies in the
// shell with "not found" (127), git reports the file conflicted, and nobody is
// told why. The fallback merges as git does without a driver; where that
// conflicts it keeps this side of a generated file, which `generate` rewrites
// from its inputs anyway, and leaves committed state (.forge/*) conflicted
// with markers, since a wrong guess there would be written to the tree and
// trusted. Either way it says which forge is gone and what repairs the driver.
func driverCommandLine(bin, name string) string {
	q := "'" + strings.ReplaceAll(bin, "'", `'\''`) + "'"
	words := strings.Fields(name)
	words[0] = q
	hint := name + " generate"
	return driverGuardPrefix + q + " ] && exec " + strings.Join(words, " ") + " merge-generated %O %A %B %L %P; " +
		`o=$(mktemp) || exit 1; cp %A "$o"; ` +
		"git merge-file --marker-size=%L -L ours -L base -L theirs %A %O %B; rc=$?; " +
		`if [ $rc -eq 0 ]; then rm -f "$o"; exit 0; fi; ` +
		"case %P in .forge/*|*/.forge/*) rm -f \"$o\"; " +
		"printf 'forge merge-generated %s: the forge this driver names (%s) is gone; this state file conflicts as git would have it. Run `" + hint + "` with the pinned forge to repair the driver.\\n' %P " + q + " >&2; exit 1;; esac; " +
		`cat "$o" > %A; rm -f "$o"; ` +
		"printf 'forge merge-generated %s: the forge this driver names (%s) is gone; kept this side of the generated file. Run `" + hint + "` with the pinned forge to render both sides and repair the driver.\\n' %P " + q + " >&2; exit 0"
}

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
	"forge_descriptor.json",
	".forge/scaffolded.json", ".forge/hashes.json", ".forge/disowned.json",
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
