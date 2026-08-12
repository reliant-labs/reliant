// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// pathFields are the JSON keys treated as filesystem paths.
//
// This layer is a fast reject, not the boundary. It inspects argument NAMES,
// so walkPayload recurses into keys it does not recognize and checks nothing
// there — a daemon command carrying a path under a key absent from this map
// passes here unexamined.
//
// The boundary is in the handlers, which call daemonpolicy.ResolveFile /
// ResolveDir immediately before touching the filesystem and resolve through
// os.Root, where the kernel enforces containment (see rootfs.go). That is what
// catches an unrecognized key, and what closes the window between this check
// and the open, during which a directory can be swapped for a symlink.
//
// The value of checking here is that an obviously-out-of-bounds request is
// refused before the daemon does any work, and the caller gets a clear reason.
// When adding a connector-reachable command, wire it to ResolveFile/ResolveDir
// — adding a key here alone is not sufficient.
var pathFields = map[string]bool{
	"path":          true,
	"file_path":     true,
	"working_dir":   true,
	"dir":           true,
	"directory":     true,
	"cwd":           true,
	"source":        true,
	"destination":   true,
	"src":           true,
	"dst":           true,
	"root":          true,
	"project_path":  true,
	"worktree_path": true,
	"repo_path":     true,
	"target":        true,
	"base_path":     true,
	"paths":         true,
	"files":         true,
	// base_dir is where fs.glob, fs.search, and fs.find_replace carry their
	// search root — nested under "opts", not at the top level. Omitting it
	// would let a connector search the whole filesystem while every top-level
	// path check passed.
	"base_dir": true,
}

// patternFields are JSON keys holding glob or file-filter patterns.
//
// These are not paths, but they reach the filesystem just as directly: ripgrep
// and the glob walker both accept absolute and parent-relative patterns, so
// `{"pattern": "/etc/**"}` reads outside the root while every path-shaped
// field in the request is empty and therefore trivially "inside" it. Confining
// base_dir alone leaves that open.
var patternFields = map[string]bool{
	"pattern":   true,
	"file_glob": true,
	"glob":      true,
	"include":   true,
	"exclude":   true,
}

// commandFields are the JSON keys holding a shell command line.
var commandFields = map[string]bool{
	"command": true,
	"cmd":     true,
}

// extractArgv reads the argv form of a run request.
//
// It returns an empty slice when the request carries no argv — including when
// it carries a shell-string command instead, which under an allowlist is a
// refusal rather than a fallback.
func extractArgv(payload []byte) ([]string, error) {
	var req struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("%w: request payload was not valid JSON", ErrDenied)
	}
	return req.Argv, nil
}

// checkPaths verifies every path-shaped value in payload resolves inside
// PathRoot.
func (p *Policy) checkPaths(payload []byte) error {
	// Resolve the root FIRST, before any early return. A policy with no root
	// grants no filesystem access at all, and that must hold even for a
	// command whose payload is empty — several handlers treat an absent path
	// as "use the daemon's working directory", which is not inside the root.
	root, err := p.resolvedRoot()
	if err != nil {
		return err
	}

	if len(payload) == 0 {
		return nil
	}

	var generic any
	if err := json.Unmarshal(payload, &generic); err != nil {
		// A payload we cannot parse is one we cannot confine. The handler is
		// about to reject it anyway, so denying loses nothing and avoids
		// guessing at a malformed request's intent.
		return fmt.Errorf("%w: request payload was not valid JSON", ErrDenied)
	}

	return walkPayload(generic, root)
}

// resolvedRoot returns PathRoot with symlinks resolved, so comparisons happen
// in the same namespace as the resolved candidate paths.
func (p *Policy) resolvedRoot() (string, error) {
	if strings.TrimSpace(p.PathRoot) == "" {
		return "", fmt.Errorf("%w: this connector has no filesystem access", ErrDenied)
	}
	abs, err := filepath.Abs(p.PathRoot)
	if err != nil {
		return "", fmt.Errorf("%w: connector path root is unusable", ErrDenied)
	}
	// The root itself is trusted config, so a resolution failure (e.g. the
	// workspace has not been cloned yet) falls back to the lexical absolute
	// path rather than failing the request.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

// walkPayload recurses through decoded JSON, checking every value found under
// a path-shaped key.
func walkPayload(node any, root string) error {
	switch v := node.(type) {
	case map[string]any:
		for key, val := range v {
			lower := strings.ToLower(key)
			if pathFields[lower] {
				if err := checkPathValue(val, root); err != nil {
					return err
				}
				continue
			}
			if patternFields[lower] {
				if err := checkPatternValue(val); err != nil {
					return err
				}
				continue
			}
			if err := walkPayload(val, root); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := walkPayload(item, root); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkPathValue confines a single value, which may be a string or a list of
// strings (fs.glob takes "paths").
//
// Anything else is denied rather than skipped. A path key holding a number, an
// object, or a nested list is not a shape this function knows how to confine,
// and silently passing it would make the confinement's coverage depend on the
// payload's type rather than on its keys.
func checkPathValue(val any, root string) error {
	switch v := val.(type) {
	case nil:
		return nil
	case string:
		return confine(v, root)
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("%w: a path list contained a non-string entry", ErrDenied)
			}
			if err := confine(s, root); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: a path field held an unexpected value type", ErrDenied)
	}
}

// checkPatternValue rejects glob patterns that reach outside the root.
//
// Unlike a path, a pattern is not resolved against the filesystem, so it is
// checked lexically: an absolute pattern or one containing a parent traversal
// would let the walker escape regardless of the search root.
func checkPatternValue(val any) error {
	switch v := val.(type) {
	case nil:
		return nil
	case string:
		return confinePattern(v)
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("%w: a pattern list contained a non-string entry", ErrDenied)
			}
			if err := confinePattern(s); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: a pattern field held an unexpected value type", ErrDenied)
	}
}

// confinePattern rejects a pattern that could match outside the search root.
func confinePattern(pattern string) error {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return nil
	}

	// Both separators are checked because a pattern is matched against paths
	// the daemon builds, and the daemon may be running on Windows.
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, `\`) {
		return fmt.Errorf("%w: pattern %q is absolute and would search outside the allowed directory",
			ErrDenied, pattern)
	}
	if len(trimmed) >= 2 && trimmed[1] == ':' {
		return fmt.Errorf("%w: pattern %q is absolute and would search outside the allowed directory",
			ErrDenied, pattern)
	}
	// Brace groups defeat segment-splitting: `{..,.}/**/*` contains no ".."
	// segment by the / delimiter, but both doublestar and ripgrep expand it
	// into one that walks upward. A connector has no need for brace groups, so
	// rejecting them outright is simpler and more durable than trying to
	// expand them here and check every branch.
	if strings.ContainsAny(trimmed, "{}") {
		return fmt.Errorf("%w: pattern %q uses brace expansion, which is not allowed for this connector",
			ErrDenied, pattern)
	}

	// Split on both separators AND the comma, so a traversal cannot hide
	// inside any grouping construct that survives the check above.
	for _, segment := range strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == '/' || r == '\\' || r == ','
	}) {
		if segment == ".." {
			return fmt.Errorf("%w: pattern %q traverses outside the allowed directory", ErrDenied, pattern)
		}
	}
	return nil
}

// confine reports whether candidate resolves inside root.
//
// Resolution happens before comparison, because a lexical check is defeated by
// a symlink inside the root pointing out of it. For a path that does not exist
// yet — every create, and every write to a new file — the nearest existing
// ancestor is resolved instead, which is what a create would actually follow.
func confine(candidate, root string) error {
	// An empty value is NOT a no-op. Handlers treat an absent path as "use the
	// daemon's working directory" or "$HOME" — fs.list_dir, fs.glob,
	// fs.search, fs.get_tree, and the worktree git commands all do — and the
	// daemon is not chdir'd into the grant's root. Treating empty as "nothing
	// to check" therefore widens access rather than leaving it unchanged, so
	// it resolves to the root and is checked like any other value.
	if strings.TrimSpace(candidate) == "" {
		candidate = root
	}

	// A relative path is interpreted against the root, which is the daemon's
	// working directory for a confined caller.
	abs := candidate
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)

	// A NUL byte truncates the path at the syscall boundary, so what the
	// kernel opens can differ from what was checked here.
	if strings.ContainsRune(abs, '\x00') {
		return fmt.Errorf("%w: path contains an invalid character", ErrDenied)
	}

	resolved, err := resolveExisting(abs)
	if err != nil {
		return fmt.Errorf("%w: path %q could not be resolved", ErrDenied, candidate)
	}

	if !within(resolved, root) {
		return fmt.Errorf("%w: path %q resolves outside this connector's allowed directory", ErrDenied, candidate)
	}
	return nil
}

// resolveExisting resolves symlinks for path, walking up to the nearest
// existing ancestor when path itself does not exist. The unresolved remainder
// is rejoined, so a create under a symlinked parent is judged by where the
// parent actually points.
func resolveExisting(path string) (string, error) {
	remainder := ""
	current := path

	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if remainder == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, remainder), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached the filesystem root without finding anything that
			// exists. Nothing to resolve, so judge the path lexically.
			return path, nil
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

// within reports whether path is root or sits beneath it. The separator check
// prevents "/workspace-other" from matching root "/workspace".
func within(path, root string) bool {
	if path == root {
		return true
	}
	sep := string(filepath.Separator)
	// The filesystem root already ends in a separator, so appending another
	// yields "//" — a prefix no path has, which silently denied every request
	// for a grant whose root is "/". A grant that names the whole filesystem
	// is a deliberate choice the UI offers; it has to mean what it says.
	if root == sep {
		return strings.HasPrefix(path, sep)
	}
	return strings.HasPrefix(path, root+sep)
}
