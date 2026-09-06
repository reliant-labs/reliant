package filepreview

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// ErrPathOutsideBase is returned when a RELATIVE path climbs out of its base
// directory. That is always a traversal attempt and is always refused.
var ErrPathOutsideBase = errors.New("requested path is outside base directory")

// ErrAbsolutePathOutsideBase is returned when an ABSOLUTE path names a location
// outside the workspace and the caller is not permitted to read host files.
//
// It is deliberately distinct from ErrPathOutsideBase. The two describe
// different acts: a relative path escaping its base is a traversal, while an
// absolute path is a location the user named outright. Callers surface them
// with different messages, so collapsing them into one sentinel would make the
// UI unable to explain which happened.
var ErrAbsolutePathOutsideBase = errors.New("absolute path is outside the workspace")

// PathScope selects how an absolute requested path is treated.
type PathScope int

const (
	// ScopeBaseOnly confines the request to the base directory. An absolute
	// path outside it is refused. This is the default and is what every
	// mutating operation uses.
	ScopeBaseOnly PathScope = iota

	// ScopeAllowAbsolute additionally permits an absolute path that names a
	// location outside the base. It is granted only to read-only viewer
	// operations, and only where the filesystem being read belongs to the
	// requesting user (see the callers in internal/grpc/services).
	ScopeAllowAbsolute
)

// ResolveBasePath resolves the correct base path for file operations.
// Returns the worktree path if worktree_id or chat_id is provided, otherwise returns project path.
func ResolveBasePath(ctx context.Context, repo db.Repository, projectID string, worktreeID *string, chatID *string) (string, error) {
	wtID := ""
	if worktreeID != nil {
		wtID = *worktreeID
	}

	if wtID == "" && chatID != nil && *chatID != "" {
		chat, err := repo.GetChat(ctx, *chatID)
		if err == nil && chat.WorktreeID != nil && *chat.WorktreeID != "" {
			wtID = *chat.WorktreeID
		}
	}

	if wtID != "" {
		worktree, err := repo.GetWorktree(ctx, wtID)
		if err == nil {
			return worktree.Path, nil
		}
	}

	project, err := repo.GetProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	return project.Path, nil
}

// ValidatePath resolves requestedPath against basePath and enforces the
// confinement rule for the base scope. It is equivalent to
// ValidatePathScoped(basePath, requestedPath, ScopeBaseOnly).
func ValidatePath(basePath, requestedPath string) (string, error) {
	return ValidatePathScoped(basePath, requestedPath, ScopeBaseOnly)
}

// ValidatePathScoped resolves requestedPath against basePath under scope.
//
// Relative paths are joined onto the base and must stay beneath it. A relative
// path that climbs out is refused with ErrPathOutsideBase under EVERY scope —
// traversal is never permitted, because a relative path is interpreted against
// a base the user did not choose, so escaping it can only be an attempt to
// reach something the base was meant to exclude.
//
// Absolute paths are NOT joined onto the base. Joining them was the previous
// behaviour and it was wrong in both directions: an absolute path already
// inside the workspace was concatenated onto the base a second time
// (/w + /w/src/a.go -> /w/w/src/a.go, a file that does not exist), and an
// absolute path outside it was silently rewritten to a different file under the
// base (/w + /etc/passwd -> /w/etc/passwd) instead of being either honoured or
// refused. An absolute path is now taken to mean the location it names:
//
//   - inside the base: always allowed, under either scope.
//   - outside the base: allowed only under ScopeAllowAbsolute, otherwise
//     refused with ErrAbsolutePathOutsideBase.
func ValidatePathScoped(basePath, requestedPath string, scope PathScope) (string, error) {
	// The base names a workspace on the DAEMON, which may run Windows while
	// this server runs Linux, so it is resolved in its own convention. Only a
	// base that is absolute under NEITHER convention is a genuinely relative
	// local path, and only then is filepath.Abs (which resolves against THIS
	// process's working directory) the right thing to do.
	//
	// Getting this wrong failed open, not closed: filepath.Abs turned
	// `C:\Users\sean\proj` into `/cwd/C:\Users\sean\proj`, filepath.IsAbs
	// judged every requested path relative, and the join below then accepted
	// `..\secret.txt` — the traversal this function exists to refuse.
	absBasePath := basePath
	if ospath.IsAbs(basePath) {
		absBasePath = ospath.Clean(basePath)
	} else {
		var err error
		if absBasePath, err = filepath.Abs(basePath); err != nil {
			return "", err
		}
	}

	// "" and "/" both mean "the workspace root itself" in this API, not the
	// filesystem root. The file tree relies on it: it defaults an unset path
	// to "/" to list the top of the workspace.
	//
	// This is the ONLY implementation of that rule. FileSystemProxyService
	// used to encode a second copy of it by hand (resolvePath in fs_proxy.go)
	// and, because the copy omitted the withinBase check below, "/" reached
	// the daemon as the literal filesystem root. It now calls this function,
	// so the rule and its confinement cannot be separated again.
	if requestedPath == "" || requestedPath == "/" {
		return absBasePath, nil
	}

	if ospath.IsAbs(requestedPath) {
		absRequested := ospath.Clean(requestedPath)
		if withinBase(absRequested, absBasePath) {
			return absRequested, nil
		}
		if scope == ScopeAllowAbsolute {
			return absRequested, nil
		}
		return "", ErrAbsolutePathOutsideBase
	}

	// ospath.Join, not filepath.Join: joining with the host separator would
	// emit `C:\proj/src\main.go`, which the daemon cannot resolve and which
	// withinBase could not compare consistently.
	absFullPath := ospath.Join(absBasePath, requestedPath)
	if !withinBase(absFullPath, absBasePath) {
		return "", ErrPathOutsideBase
	}
	return absFullPath, nil
}

// withinBase reports whether path is base itself or a descendant of it. The
// separator guards against /projects/app matching /projects/app-secrets.
//
// Both separators are accepted because base may be a Windows path: after
// ospath.Clean a `C:/...` base keeps forward slashes while a `C:\...` base
// keeps backslashes, and the two must confine identically.
func withinBase(path, base string) bool {
	return path == base ||
		strings.HasPrefix(path, base+"/") ||
		strings.HasPrefix(path, base+`\`)
}
