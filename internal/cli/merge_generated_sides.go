package cli

// The sides of the merge git is running, for the one decision the driver
// cannot make from the three blobs git hands it: whether a conflicting
// uncertified aggregate (checksums.Uncertified: gen/forge_service_catalog.json)
// may be settled by keeping this side.
//
// WHY. The catalog is a projection of the whole project, so two branches that
// each add a service both insert into the same lists and conflict as lines.
// It is regenerated whole on every `forge generate`, so the principled
// resolution is the one every other projection gets: keep this side and
// regenerate. The one thing that must not happen is dropping a person's
// edit, and a person's edit of the catalog survives only where the catalog is
// disowned. So the driver keeps this side only when it can read the disown
// record of base, ours AND theirs, and none of them disowns the catalog.
//
// Git passes a driver no commit names. It does leave enough to find them:
//
//   - `git merge` (and `git pull`) names the commit it merges in the
//     GITHEAD_<oid> environment variable, for its conflict labels, before any
//     driver runs. MERGE_HEAD is written only afterwards.
//   - `git rebase` (the merge backend) appends the pick it is applying to
//     rebase-merge/done before applying it, and HEAD is the side it applies
//     onto. Its bases are the pick's parent (the base git merges against)
//     and every fork point of HEAD and the pick.
//
// Every other operation (a cherry-pick, `git merge-tree`, `git am`) names
// nothing, so the conflict stays, as before. A named commit is trusted only
// when its copy of the file is byte-for-byte the blob git handed the driver,
// and HEAD's copy is ours, so a stale variable or a leftover rebase state can
// never stand in for the real side.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// regenerateNote is what the driver prints when it keeps this side of a
// conflicting uncertified projection.
const regenerateNote = "kept this side's copy: both branches changed this generated projection, which forge regenerates rather than merges; run `forge generate` to rebuild it from both sides' inputs (`forge ci verify-generated` fails until you do)"

// mergeSideDisownRecords returns the .forge/disowned.json bytes (nil where a
// commit has none) of every commit on every side of the merge git is running
// over relPath. known is false when a side cannot be named and verified. A
// variable so the driver's decision is testable without a real merge.
var mergeSideDisownRecords = gitMergeSideDisownRecords

// uncertifiedConflictKeepsOurs reports whether a line merge of relPath that
// conflicted may be settled by keeping ours: relPath is an output nothing
// certifies, and no side of the merge disowns it.
func uncertifiedConflictKeepsOurs(ctx context.Context, relPath string, ours, theirs []byte) bool {
	rel, ok := projectRelative(relPath)
	if !ok || !checksums.Uncertified(rel) {
		return false
	}
	records, known := mergeSideDisownRecords(ctx, relPath, ours, theirs)
	return known && !disownedOnAnySide(rel, records)
}

// disownedOnAnySide reports whether any record disowns rel. A record that
// does not parse counts as disowning it: it cannot prove the file is forge's.
func disownedOnAnySide(rel string, records [][]byte) bool {
	for _, r := range records {
		if disowned, ok := checksums.DisownedIn(r, rel); disowned || !ok {
			return true
		}
	}
	return false
}

// gitMergeSideDisownRecords is mergeSideDisownRecords against the repository
// git is merging in (the driver runs at the top of its work tree).
func gitMergeSideDisownRecords(ctx context.Context, relPath string, ours, theirs []byte) ([][]byte, bool) {
	rel, ok := projectRelative(relPath)
	if !ok {
		return nil, false
	}
	prefix := strings.TrimSuffix(filepath.ToSlash(relPath), rel)
	theirsCommit, bases, ok := currentMergeSides(ctx)
	if !ok {
		return nil, false
	}
	file := prefix + rel
	if got, ok := gitFileAt(ctx, "HEAD", file); !ok || !bytes.Equal(got, ours) {
		return nil, false
	}
	if got, ok := gitFileAt(ctx, theirsCommit, file); !ok || !bytes.Equal(got, theirs) {
		return nil, false
	}
	var records [][]byte
	for _, c := range append([]string{"HEAD", theirsCommit}, bases...) {
		r, ok := gitFileAt(ctx, c, prefix+checksums.DisownedFile)
		if !ok {
			return nil, false
		}
		records = append(records, r)
	}
	return records, true
}

// currentMergeSides names the commit being merged into HEAD and its merge
// bases. ok is false when the running operation does not name it.
func currentMergeSides(ctx context.Context) (theirs string, bases []string, ok bool) {
	var heads []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if oid, found := strings.CutPrefix(k, "GITHEAD_"); found && oid != "" {
			heads = append(heads, oid)
		}
	}
	if len(heads) > 1 {
		return "", nil, false // an octopus: no single other side
	}
	if len(heads) == 1 {
		theirs, ok = resolveCommit(ctx, heads[0])
		if !ok {
			return "", nil, false
		}
		// Unrelated histories have no merge base: an empty list, not an error.
		out, err := exec.CommandContext(ctx, "git", "merge-base", "--all", "HEAD", theirs).Output()
		var exitErr *exec.ExitError
		if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1) {
			return "", nil, false
		}
		return theirs, strings.Fields(string(out)), true
	}
	theirs, ok = rebasePickInProgress(ctx)
	if !ok {
		return "", nil, false
	}
	// A pick applies the commit's change against its parent: that is the
	// base git hands the driver. The onto history may have diverged from
	// that parent, so the fork point(s) of HEAD and the pick are bases too:
	// a catalog disowned there may still hold a person's content, and every
	// base must be read before the conflict is settled.
	out, err := exec.CommandContext(ctx, "git", "rev-list", "--parents", "-n", "1", theirs).Output()
	if err != nil {
		return "", nil, false
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil, false
	}
	bases = fields[1:]
	forks, err := exec.CommandContext(ctx, "git", "merge-base", "--all", "HEAD", theirs).Output()
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1) {
		return "", nil, false
	}
	for _, f := range strings.Fields(string(forks)) {
		if !slices.Contains(bases, f) {
			bases = append(bases, f)
		}
	}
	return theirs, bases, true
}

// rebasePickInProgress is the commit a merge-backend rebase is applying: the
// last line of rebase-merge/done, when it is a command that picks one commit.
func rebasePickInProgress(ctx context.Context) (string, bool) {
	p, err := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-path", "rebase-merge/done").Output()
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(strings.TrimSpace(string(p)))
	if err != nil {
		return "", false
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return "", false // no pick recorded yet
	}
	lines := strings.Split(trimmed, "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 2 {
		return "", false
	}
	switch fields[0] {
	case "pick", "p", "reword", "r", "edit", "e", "fixup", "f", "squash", "s":
	default:
		return "", false // merge, exec, label, …: not one commit's change
	}
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "-") {
			continue // fixup -C / -c
		}
		return resolveCommit(ctx, f)
	}
	return "", false
}

// resolveCommit is rev's full commit id.
func resolveCommit(ctx context.Context, rev string) (string, bool) {
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", rev+"^{commit}").Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// gitFileAt is file's content in commit: nil when the commit has no such
// file. ok is false when git cannot answer.
func gitFileAt(ctx context.Context, commit, file string) ([]byte, bool) {
	spec := commit + ":" + file
	err := exec.CommandContext(ctx, "git", "cat-file", "-e", spec).Run()
	if err != nil {
		// cat-file -e exits non-zero for a missing path; tell that apart
		// from a missing commit, which is not an answer.
		if _, ok := resolveCommit(ctx, commit); !ok {
			return nil, false
		}
		return nil, true
	}
	out, err := exec.CommandContext(ctx, "git", "cat-file", "blob", spec).Output()
	if err != nil {
		return nil, false
	}
	if out == nil {
		out = []byte{} // present but empty is not "no such file"
	}
	return out, true
}
