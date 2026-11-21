package buildtarget

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MatchOutputs resolves one declared build output (kcl/schema.k BuildOutput)
// against dir, the build's working directory, and returns the regular files
// it selects as slash-separated paths relative to dir, sorted.
//
// The pattern grammar is the one ShellBuild.inputs uses: `*`, `?` and
// `[...]` match within one path segment, a whole `**` segment matches any
// number of segments, and a pattern without metacharacters naming a
// directory selects every file beneath it. Unlike inputs, an output never
// leaves dir (no absolute path, no `..`), symlinks are not followed or
// selected (a release records the bytes the build wrote, not what a link
// points at), and a pattern that selects nothing is NOT an error here: the
// caller decides whether a missing output fails the build or warns.
func MatchOutputs(dir, pattern string) ([]string, error) {
	p, err := parseOutputPattern(pattern)
	if err != nil {
		return nil, err
	}
	// The working directory itself may be reached through a link (a
	// sibling checkout linked into place); only what lies beneath it is
	// held to "no symlinks".
	if real, rerr := filepath.EvalSymlinks(dir); rerr == nil {
		dir = real
	} else if errors.Is(rerr, fs.ErrNotExist) {
		return nil, nil
	} else {
		return nil, rerr
	}
	start := filepath.Join(dir, filepath.FromSlash(p.root))
	info, err := os.Lstat(start)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode().IsRegular() {
		if p.matches(p.root) {
			return []string{p.root}, nil
		}
		return nil, nil
	}
	if !info.IsDir() {
		return nil, nil
	}
	var out []string
	err = filepath.WalkDir(start, func(abs string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(dir, abs)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if abs != start && !p.mayMatchBelow(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && p.matches(rel) {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("output %q: %w", pattern, err)
	}
	sort.Strings(out)
	return out, nil
}

// parseOutputPattern parses an output pattern with the inputs grammar and
// refuses what an output may not name: an absolute path, a `..` segment, a
// negation, or the whole working directory.
func parseOutputPattern(raw string) (inputPattern, error) {
	s := filepath.ToSlash(strings.TrimSpace(raw))
	switch {
	case s == "":
		return inputPattern{}, fmt.Errorf("output path is empty")
	case strings.HasPrefix(s, "!"):
		return inputPattern{}, fmt.Errorf("output %q: an output names files the build writes; it has no `!` exclusion", raw)
	case path.IsAbs(s) || filepath.IsAbs(raw) || (len(s) >= 2 && s[1] == ':'):
		return inputPattern{}, fmt.Errorf("output %q is absolute; outputs are relative to the build's working directory", raw)
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return inputPattern{}, fmt.Errorf("output %q leaves the build's working directory", raw)
		}
	}
	if path.Clean(s) == "." {
		return inputPattern{}, fmt.Errorf("output %q names the whole working directory; name the files the release ships", raw)
	}
	return parseInputPattern(s)
}
