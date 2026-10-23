package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cliutil"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// `forge project new --in-place` scaffolds into a directory the user already
// owns — very often an existing product repository being converted to forge.
// Two rules keep that safe, and both are enforced here rather than at the
// dozens of individual write sites inside the generator:
//
//  1. Nothing that already exists is overwritten. The scaffold is rendered
//     into a private STAGING directory and then merged into the target by
//     mergeScaffoldInto, the single place an in-place scaffold writes the
//     user's tree. A file forge would write that is already present is kept
//     and reported; only --force replaces it. A new generator write site
//     is therefore covered automatically instead of needing its own guard.
//  2. .gitignore is merged, never replaced. The user's entries are what keep
//     their untracked artefacts out of commits; dropping them is how an
//     earlier version swept .agent-artifacts/ and .reliant/ into a commit.
//     And forge never adds a pattern that matches a file the repository
//     TRACKS: forge's template ignores a root /package.json and go.work.sum,
//     which an existing repo may well commit (reliant tracks both). See
//     dropTrackedIgnores.
//
// Git is handled in finalizeNewProject: a target inside an existing work tree
// is never init'ed, configured, staged or committed (see gitWorkTreeRoot).

// Delimiters of the block mergeGitignore appends to a user's .gitignore. The
// block is rewritten wholesale on every in-place run, which is what makes the
// merge idempotent.
const (
	forgeGitignoreBegin = "# --- forge ---"
	forgeGitignoreEnd   = "# --- end forge ---"
)

// inPlaceMergeResult reports what mergeScaffoldInto did with files that
// already existed in the target.
type inPlaceMergeResult struct {
	// kept lists pre-existing files (slash-separated, relative to the target)
	// that were left untouched, so forge's version of them was NOT written.
	kept []string
	// overwritten lists pre-existing files replaced because of --force.
	overwritten []string
	// gitignoreMerged is true when a pre-existing .gitignore was merged;
	// gitignoreAdded is how many forge entries the merge appended.
	gitignoreMerged bool
	gitignoreAdded  int
	// gitignoreSkipped lists the forge patterns left out because they match
	// files the target's repository tracks.
	gitignoreSkipped []skippedIgnore
}

// skippedIgnore is one forge .gitignore pattern an in-place scaffold left
// out, and the tracked files it would have matched.
type skippedIgnore struct {
	pattern string
	tracked []string // slash-separated, relative to the target
}

// mergeScaffoldInto copies every file under staging into target.
//
// A file that does not exist in target is written. One that does is kept
// (and listed) unless overwrite is set. .gitignore is always merged via
// mergeGitignore. Symlinks are not copied: the only one a scaffold emits is
// the dev web-runtime bridge, which is relative to where it was written and
// is re-laid in the target by generator.EnsureDevWebRuntimeLink.
func mergeScaffoldInto(staging, target string, overwrite bool) (inPlaceMergeResult, error) {
	var res inPlaceMergeResult
	err := filepath.WalkDir(staging, func(src string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(staging, src)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dest := filepath.Join(target, rel)
		relSlash := filepath.ToSlash(rel)

		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return nil
		case d.IsDir():
			if info, statErr := os.Stat(dest); statErr == nil && !info.IsDir() {
				// The user has a FILE where forge wants a directory. Keep
				// theirs and skip everything forge would put beneath it.
				res.kept = append(res.kept, relSlash)
				return filepath.SkipDir
			}
			return os.MkdirAll(dest, 0o755)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		content, err := os.ReadFile(src)
		if err != nil {
			return err
		}

		existing, readErr := os.ReadFile(dest)
		if relSlash == ".gitignore" && (readErr == nil || os.IsNotExist(readErr)) {
			forgeBody, skipped := dropTrackedIgnores(string(content), gitTrackedIgnoreMatcher(target))
			res.gitignoreSkipped = skipped
			if readErr != nil {
				return atomicfile.WriteFile(dest, []byte(forgeBody), info.Mode().Perm())
			}
			merged, added := mergeGitignore(string(existing), forgeBody)
			res.gitignoreMerged = true
			res.gitignoreAdded = added
			if merged == string(existing) {
				return nil
			}
			return atomicfile.WriteFile(dest, []byte(merged), 0o644)
		}
		switch {
		case readErr == nil && !overwrite:
			res.kept = append(res.kept, relSlash)
			return nil
		case readErr == nil:
			res.overwritten = append(res.overwritten, relSlash)
		case !os.IsNotExist(readErr):
			if info, statErr := os.Stat(dest); statErr == nil && info.IsDir() {
				// A directory where forge wants a file: keep it.
				res.kept = append(res.kept, relSlash)
				return nil
			}
			return fmt.Errorf("read %s: %w", dest, readErr)
		}
		return atomicfile.WriteFile(dest, content, info.Mode().Perm())
	})
	sort.Strings(res.kept)
	sort.Strings(res.overwritten)
	return res, err
}

// mergeGitignore returns user's .gitignore with forge's missing entries
// appended in a delimited block, plus how many entries that block holds.
//
// Idempotent: a block from a previous run is removed before the missing set
// is computed, so re-running yields byte-identical output. A pattern already
// present outside the block is not repeated. Negations ("!foo") are always
// kept, because their effect depends on following the pattern they re-include
// — dropping one because the user happens to spell it too could change what
// is ignored.
func mergeGitignore(user, forge string) (string, int) {
	rest := stripForgeGitignoreBlock(user)

	present := map[string]bool{}
	for _, line := range strings.Split(rest, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			present[l] = true
		}
	}
	var missing []string
	seen := map[string]bool{}
	for _, line := range strings.Split(forge, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "#") || seen[l] {
			continue
		}
		seen[l] = true
		if present[l] && !strings.HasPrefix(l, "!") {
			continue
		}
		missing = append(missing, l)
	}
	if len(missing) == 0 {
		return rest, 0
	}

	var b strings.Builder
	b.WriteString(rest)
	if rest != "" {
		b.WriteString("\n")
	}
	b.WriteString(forgeGitignoreBegin + "\n")
	b.WriteString("# Entries from forge's scaffold .gitignore that this file lacked.\n")
	b.WriteString("# Written by `forge project new --in-place`; a re-run rewrites this block.\n")
	for _, l := range missing {
		b.WriteString(l + "\n")
	}
	b.WriteString(forgeGitignoreEnd + "\n")
	return b.String(), len(missing)
}

// stripForgeGitignoreBlock removes a block mergeGitignore wrote earlier, and
// the single blank separator line before it, returning the rest normalised
// to end in exactly one newline (or empty).
func stripForgeGitignoreBlock(body string) string {
	lines := strings.Split(body, "\n")
	var out []string
	inBlock := false
	for _, line := range lines {
		switch trimmed := strings.TrimSpace(line); {
		case trimmed == forgeGitignoreBegin:
			inBlock = true
			if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" {
				out = out[:n-1]
			}
		case inBlock && trimmed == forgeGitignoreEnd:
			inBlock = false
		case !inBlock:
			out = append(out, line)
		}
	}
	rest := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if rest == "" {
		return ""
	}
	return rest + "\n"
}

// trackedIgnoreMatcher returns, for each gitignore pattern (as written in the
// target's own .gitignore), the tracked files it matches. A pattern that
// matches nothing is absent from the map.
type trackedIgnoreMatcher func(patterns []string) map[string][]string

// dropTrackedIgnores removes from forge's .gitignore body every pattern that
// matches a file the repository tracks, leaving a comment in its place, and
// reports what it removed.
//
// Ignoring a tracked file does not untrack it, which is exactly why it is
// wrong: the file keeps being committed while ripgrep, editors' search and
// every other tool that honours .gitignore pretend it is not there, and the
// first time a branch or `git rm` untracks it, re-creating it is silently left
// out of `git add`.
// forge's template ignores a root /package.json, /package-lock.json and
// go.work.sum because a FORGE project never commits them; an existing
// repository adopting forge may (reliant tracks all three), and its own
// layout wins.
//
// Negations are never dropped: they only ever un-ignore. A nil match leaves
// body untouched — the target is not in a git work tree, so nothing is
// tracked.
func dropTrackedIgnores(body string, match trackedIgnoreMatcher) (string, []skippedIgnore) {
	if match == nil {
		return body, nil
	}
	lines := strings.Split(body, "\n")
	var patterns []string
	for _, line := range lines {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "!") {
			patterns = append(patterns, l)
		}
	}
	matched := match(patterns)
	if len(matched) == 0 {
		return body, nil
	}
	var skipped []skippedIgnore
	reported := map[string]bool{}
	for i, line := range lines {
		l := strings.TrimSpace(line)
		tracked, ok := matched[l]
		if !ok {
			continue
		}
		lines[i] = fmt.Sprintf("# forge left out %q: this repository tracks %s", l, summarizeTracked(tracked))
		if !reported[l] { // a pattern the template repeats is reported once
			reported[l] = true
			skipped = append(skipped, skippedIgnore{pattern: l, tracked: tracked})
		}
	}
	return strings.Join(lines, "\n"), skipped
}

// gitTrackedIgnoreMatcher matches gitignore patterns against the files the
// git work tree containing target tracks, using git's own matcher
// (`git ls-files --cached --ignored --exclude=<pattern>`), so anchoring,
// globs and directory patterns mean exactly what they will mean in the
// merged .gitignore. Nil when target is not inside a work tree.
func gitTrackedIgnoreMatcher(target string) trackedIgnoreMatcher {
	top, ok := gitWorkTreeRoot(context.Background(), target)
	if !ok {
		return nil
	}
	prefix, err := filepath.Rel(resolvePathForCompare(top), resolvePathForCompare(target))
	if err != nil {
		return nil
	}
	prefix = filepath.ToSlash(prefix)
	if prefix == "." {
		prefix = ""
	}
	lsIgnored := func(patterns []string) []string {
		args := []string{"ls-files", "-z", "--cached", "--ignored"}
		for _, p := range patterns {
			args = append(args, "--exclude="+anchorIgnorePattern(prefix, p))
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = target
		out, err := cmd.Output()
		if err != nil {
			return nil
		}
		var files []string
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				files = append(files, f)
			}
		}
		return files
	}
	return func(patterns []string) map[string][]string {
		// One call answers the common case — nothing tracked is matched —
		// before paying one call per pattern to say which pattern matched.
		if len(patterns) == 0 || len(lsIgnored(patterns)) == 0 {
			return nil
		}
		out := map[string][]string{}
		for _, p := range patterns {
			if files := lsIgnored([]string{p}); len(files) > 0 {
				out[p] = files
			}
		}
		return out
	}
}

// anchorIgnorePattern rewrites a pattern written in <prefix>/.gitignore into
// the form `git ls-files --exclude` reads it in, which is relative to the
// repository root rather than to the directory git runs in. A pattern with a
// slash before its end is anchored to its .gitignore's directory, so it gains
// the prefix; one without matches at any depth, and running ls-files from the
// target already limits it to the target's files.
func anchorIgnorePattern(prefix, pattern string) string {
	if prefix == "" || !strings.Contains(strings.TrimSuffix(pattern, "/"), "/") {
		return pattern
	}
	return "/" + prefix + "/" + strings.TrimPrefix(pattern, "/")
}

// summarizeTracked names up to three of a pattern's tracked files.
func summarizeTracked(files []string) string {
	const limit = 3
	if len(files) <= limit {
		return strings.Join(files, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(files[:limit], ", "), len(files)-limit)
}

// inPlaceMergeSummary renders what an in-place scaffold did with the user's
// existing files. Empty when nothing pre-existed.
func inPlaceMergeSummary(r inPlaceMergeResult) []string {
	var out []string
	if len(r.kept) > 0 {
		out = append(out, "",
			fmt.Sprintf("📁 Kept your existing: %s", summarizePaths(r.kept)),
			"    forge did not write its version of these. Re-run with --force to replace them.")
	}
	if len(r.overwritten) > 0 {
		out = append(out, "",
			fmt.Sprintf("⚠️  --force replaced your existing: %s", summarizePaths(r.overwritten)))
	}
	if r.gitignoreMerged {
		if r.gitignoreAdded > 0 {
			out = append(out, fmt.Sprintf("📁 Merged .gitignore: kept your entries, appended %d forge entries under %q.",
				r.gitignoreAdded, forgeGitignoreBegin))
		} else {
			out = append(out, "📁 .gitignore already covers everything forge ignores — left as is.")
		}
	}
	if len(r.gitignoreSkipped) > 0 {
		out = append(out, fmt.Sprintf("📁 Left %d forge .gitignore pattern(s) out because they match files this repository tracks:",
			len(r.gitignoreSkipped)))
		for _, s := range r.gitignoreSkipped {
			out = append(out, fmt.Sprintf("    %s  (tracks %s)", s.pattern, summarizeTracked(s.tracked)))
		}
		out = append(out, "    Ignoring a tracked file does not untrack it; it hides the file from every tool that honours .gitignore,",
			"    and from `git add` the first time a branch or `git rm` untracks it.")
	}
	return out
}

// maxSummaryPaths caps how many paths one summary line names; a re-run over
// an earlier scaffold collides on every file and would otherwise print ~100.
const maxSummaryPaths = 12

func summarizePaths(paths []string) string {
	if len(paths) <= maxSummaryPaths {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s, and %d more (%d files)",
		strings.Join(paths[:maxSummaryPaths], ", "), len(paths)-maxSummaryPaths, len(paths))
}

// dirHoldsProject reports whether an in-place target already holds something
// besides its .git directory — that is, whether `--in-place` is adopting an
// existing project rather than scaffolding a new one into an empty checkout.
//
// It deliberately does not ask WHAT the content is. The decision it feeds
// (the typed-config guardrail's starting strictness) is cheap to get wrong
// in one direction and costly in the other: a near-empty checkout read as an
// adoption starts advisory and is one edit from gating, while an adopted
// project read as greenfield fails its first `forge lint` on every env read
// it already has.
func dirHoldsProject(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			return true
		}
	}
	return false
}

// gitWorkTreeRoot reports whether dir is inside an existing git work tree —
// the repository root or any subdirectory of it — and returns that tree's
// top level. A missing git binary, or a dir outside any repository, reports
// false.
func gitWorkTreeRoot(ctx context.Context, dir string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "true" {
		return "", false
	}
	return strings.TrimSpace(lines[1]), true
}

// resolvePathForCompare canonicalises p so two spellings of one directory
// (macOS's /var vs /private/var) compare equal. Best-effort.
func resolvePathForCompare(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}

// existingFrontendApp is a JS app found in a directory being scaffolded in
// place.
type existingFrontendApp struct {
	name string // the frontend name it would take in forge.yaml
	dir  string // slash-separated, relative to the project root
	typ  string // the inventory type: "nextjs" or "vite-spa"
}

func (a existingFrontendApp) framework() string {
	if a.typ == "nextjs" {
		return "Next.js"
	}
	return "Vite"
}

// detectExistingFrontendApps finds Next.js / Vite apps at the conventional
// top-level locations (web/, frontend/, frontends/<name>/): a package.json
// that depends on next or vite.
func detectExistingFrontendApps(root string) []existingFrontendApp {
	candidates := []string{"web", "frontend"}
	if entries, err := os.ReadDir(filepath.Join(root, "frontends")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				candidates = append(candidates, "frontends/"+e.Name())
			}
		}
	}
	return detectExistingFrontendAppsIn(root, candidates)
}

// detectExistingFrontendAppsIn is detectExistingFrontendApps over an explicit
// candidate list ("." is the root itself: a static site's app).
func detectExistingFrontendAppsIn(root string, candidates []string) []existingFrontendApp {
	var apps []existingFrontendApp
	for _, dir := range candidates {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), "package.json"))
		if err != nil {
			continue
		}
		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(data, &pkg) != nil {
			continue
		}
		has := func(dep string) bool {
			_, a := pkg.Dependencies[dep]
			_, b := pkg.DevDependencies[dep]
			return a || b
		}
		typ := ""
		switch {
		case has("next"):
			typ = "nextjs"
		case has("vite"):
			typ = "vite-spa"
		default:
			continue
		}
		apps = append(apps, existingFrontendApp{name: filepath.Base(dir), dir: dir, typ: typ})
	}
	return apps
}

// adoptFrontendSnippet is the KCL declaration that points forge at an
// existing app instead of scaffolding a new one. Frontends are declared in
// each env's deploy/kcl/<env>/main.k (forge.yaml has no `frontends:` key);
// the binding (`runtime`) is that env's choice.
func adoptFrontendSnippet(a existingFrontendApp) string {
	typ, publicDir := "nextjs", "out"
	if a.typ == "vite-spa" {
		typ, publicDir = "vite", "dist"
	}
	return fmt.Sprintf("forge.Frontend {\n    name = %q\n    type = %q\n    path = %q\n    public_dir = %q\n}", a.name, typ, a.dir, publicDir)
}

// checkInPlaceFrontendCollisions refuses, before anything is written, an
// in-place --frontend <name> when an app named <name> already exists at a
// conventional location. Scaffolding it would add a second app at
// frontends/<name>/ beside the user's (or mix forge's files into it).
func checkInPlaceFrontendCollisions(apps []existingFrontendApp, frontendNames []string) error {
	for _, want := range frontendNames {
		for _, a := range apps {
			if a.name != want {
				continue
			}
			return cliutil.UserErr(Name()+" project new --in-place",
				fmt.Sprintf("--frontend %s: %s/ already holds a %s app, and scaffolding one would put a second app "+
					"at frontends/%s/ beside it. Nothing was written", want, a.dir, a.framework(), want),
				"",
				fmt.Sprintf("drop --frontend %s and declare the existing app in each env's deploy/kcl/<env>/main.k "+
					"frontends list — its path points forge at an existing directory:\n\n%s\n", want, indent(adoptFrontendSnippet(a), "    ")))
		}
	}
	return nil
}

// existingFrontendNote tells the user about apps an in-place scaffold found
// but did not touch, and how to hand them to forge.
func existingFrontendNote(apps []existingFrontendApp) []string {
	var out []string
	for _, a := range apps {
		out = append(out, "",
			fmt.Sprintf("ℹ️  Found an existing %s app at %s/ — forge left it alone. To have forge manage it,", a.framework(), a.dir),
			"    declare it in each env's deploy/kcl/<env>/main.k frontends list (path points forge at it):",
			indent(adoptFrontendSnippet(a), "      "))
	}
	return out
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
