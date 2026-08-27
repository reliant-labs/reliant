package filepreview

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
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
	absBasePath, err := filepath.Abs(basePath)
	if err != nil {
		return "", err
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

	if filepath.IsAbs(requestedPath) {
		absRequested := filepath.Clean(requestedPath)
		if withinBase(absRequested, absBasePath) {
			return absRequested, nil
		}
		if scope == ScopeAllowAbsolute {
			return absRequested, nil
		}
		return "", ErrAbsolutePathOutsideBase
	}

	absFullPath, err := filepath.Abs(filepath.Join(absBasePath, requestedPath))
	if err != nil {
		return "", err
	}
	if !withinBase(absFullPath, absBasePath) {
		return "", ErrPathOutsideBase
	}
	return absFullPath, nil
}

// withinBase reports whether path is base itself or a descendant of it. The
// separator guards against /projects/app matching /projects/app-secrets.
func withinBase(path, base string) bool {
	return path == base || strings.HasPrefix(path, base+string(filepath.Separator))
}
