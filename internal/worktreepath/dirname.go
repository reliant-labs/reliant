// Package worktreepath builds filesystem-friendly directory names for
// worktree workspaces. The chosen path becomes part of the absolute path
// the user sees on disk, so it should be readable, scoped by project,
// and disambiguated against orphans from previously-deleted worktrees.
package worktreepath

import (
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const maxSlugLen = 40

var nonSlugRE = regexp.MustCompile(`[^a-z0-9]+`)

// WorkspaceDirName returns the directory segment placed under
// `<HOME>/.reliant/worktrees/` for a new worktree workspace:
//
//	<project-slug>/<worktree-slug>-<short-id>
//
// The 8-char short-id keeps the path unique even if the user reuses a
// worktree name after deleting an old one whose directory wasn't cleaned.
func WorkspaceDirName(projectName, worktreeName string) string {
	proj := slug(projectName)
	if proj == "" {
		proj = "workspace"
	}
	wt := slug(worktreeName)
	if wt == "" {
		wt = "worktree"
	}
	short := strings.SplitN(uuid.New().String(), "-", 2)[0]
	return path.Join(proj, wt+"-"+short)
}

func slug(name string) string {
	s := nonSlugRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if len(s) > maxSlugLen {
		s = strings.Trim(s[:maxSlugLen], "-")
	}
	return s
}
