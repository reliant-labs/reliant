package lint

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// forge projects do not use .env files.
//
// A dotenv was handed to every host service WHOLESALE, so a value added to
// it went live in every process without ever being declared in KCL. That
// made "append a line to the untracked file" strictly cheaper than
// declaring the value, and the predictable result was non-secret config —
// service URLs, client IDs, issuer names — drifting out of version control
// into a file nobody could review or reproduce.
//
// The replacement is `forge.FileSecrets`: a single gitignored YAML file,
// populated with `forge secret set`, injected ONLY into the services that
// declare each key. This check keeps the old pattern from
// creeping back — including via a stray file a developer copies from an
// older project.
//
// Skipped directories mirror the rest of the lint sweep: vendored trees
// and build output are not the project's own source.
var dotenvSkipDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	".next":        true,
	"target":       true,
	".forge":       true,
}

// dotenvFinding is one offending file, relative to the project root.
type dotenvFinding struct {
	Path string
	Why  string
}

// findDotenvFiles walks root and returns every .env* file that is not
// explicitly allowed.
//
// `.env.example` is NOT allowed either: its whole purpose was to document
// the keys of a file that should no longer exist, and `forge secret ensure`
// now reports exactly which secrets an env needs — from the KCL
// declarations, so it cannot drift.
func findDotenvFiles(root string) ([]dotenvFinding, error) {
	var found []dotenvFinding

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree is not a lint failure; skip it.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if dotenvSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if name != ".env" && !strings.HasPrefix(name, ".env.") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		found = append(found, dotenvFinding{
			Path: rel,
			Why:  classifyDotenv(name),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	found = dropDotenvsInIgnoredDirs(root, found)
	sort.Slice(found, func(i, j int) bool { return found[i].Path < found[j].Path })
	return found, nil
}

// dropDotenvsInIgnoredDirs removes findings that live inside a DIRECTORY the
// project's .gitignore excludes — build output, tool caches, a vendored
// checkout. Those are not the project's own source, exactly like the
// hard-coded dotenvSkipDirs, but a project names them in .gitignore rather
// than in forge.
//
// The line is drawn at the directory on purpose. A `.env` that is ignored by
// its OWN name (`.env` in .gitignore) is the untracked dotenv this lint
// exists to catch — gitignoring it is how that pattern always looked — so it
// is still reported. A TRACKED file is always reported, whatever .gitignore
// says: it is in version control, so it is the project's.
//
// Outside a git work tree (or with no git binary) nothing is dropped: the
// lint stays as strict as it was rather than guessing.
func dropDotenvsInIgnoredDirs(root string, found []dotenvFinding) []dotenvFinding {
	if len(found) == 0 {
		return found
	}
	tracked, ok := gitTrackedPaths(root, found)
	if !ok {
		return found
	}
	// Every ancestor directory of every untracked finding, asked in one batch.
	var dirs []string
	seen := map[string]bool{}
	for _, f := range found {
		if tracked[filepath.ToSlash(f.Path)] {
			continue
		}
		for d := filepath.ToSlash(filepath.Dir(f.Path)); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			if !seen[d] {
				seen[d] = true
				dirs = append(dirs, d+"/")
			}
		}
	}
	if len(dirs) == 0 {
		return found
	}
	ignoredDirs, ok := gitIgnoredPaths(root, dirs)
	if !ok {
		return found
	}
	out := found[:0]
	for _, f := range found {
		p := filepath.ToSlash(f.Path)
		if !tracked[p] && hasIgnoredAncestor(p, ignoredDirs) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func hasIgnoredAncestor(p string, ignoredDirs map[string]bool) bool {
	for d := path.Dir(p); d != "." && d != "/" && d != ""; d = path.Dir(d) {
		if ignoredDirs[d+"/"] {
			return true
		}
	}
	return false
}

// gitTrackedPaths returns which of found are tracked by git (slash paths
// relative to root). ok is false when root is not inside a git work tree.
func gitTrackedPaths(root string, found []dotenvFinding) (map[string]bool, bool) {
	args := []string{"-C", root, "ls-files", "-z", "--"}
	for _, f := range found {
		args = append(args, filepath.ToSlash(f.Path))
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, false
	}
	tracked := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			tracked[p] = true
		}
	}
	return tracked, true
}

// gitIgnoredPaths runs `git check-ignore` over paths and returns the subset
// .gitignore excludes. --no-index judges each path by the ignore rules alone,
// independent of whether something beneath it is tracked. Exit status 1 means
// "none ignored" and is not an error.
func gitIgnoredPaths(root string, paths []string) (map[string]bool, bool) {
	cmd := exec.Command("git", "-C", root, "check-ignore", "--no-index", "--stdin", "-z")
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return nil, false
		}
	}
	ignored := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			ignored[p] = true
		}
	}
	return ignored, true
}

// classifyDotenv gives the file-specific reason + fix, so the message is
// actionable rather than a blanket "delete this".
func classifyDotenv(name string) string {
	switch {
	case strings.HasSuffix(name, ".example"):
		return "documents keys that should be declared in KCL; `forge secret ensure --env <env>` lists them from the declarations instead"
	case strings.Contains(name, "secret"):
		return "secret values belong in the secret store: `forge secret migrate --env <env>`"
	default:
		return "config belongs in deploy/kcl/<env>/config.k; secrets in the secret store (`forge secret migrate --env <env>`)"
	}
}

// collectNoDotenvJSON is the JSON-mode view of the same check.
func collectNoDotenvJSON(root string) ([]lintJSONFinding, error) {
	if root == "" {
		return nil, nil
	}
	found, err := findDotenvFiles(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan for .env files: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(found))
	for _, f := range found {
		out = append(out, lintJSONFinding{
			Rule:     "no-dotenv",
			Severity: "error",
			File:     f.Path,
			Message:  fmt.Sprintf(".env files are not used by forge projects: %s", f.Why),
			FixHint: "a dotenv is injected into every host service wholesale, so its values never have to be " +
				"declared in KCL — which is how config stops being reproducible. Declare secrets with " +
				"`EnvVar.secret_ref` and store the values with `forge secret set --env <env> <KEY>`",
		})
	}
	return out, nil
}
