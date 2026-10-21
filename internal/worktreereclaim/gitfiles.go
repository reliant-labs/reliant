// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A linked worktree's registration is a handful of small files, and the
// reaper reads it on every check: the checkout's .git file names its admin
// directory (<common>/worktrees/<name>), whose `commondir` names the parent
// repository and whose `gitdir` and `locked` are what `git worktree list`
// reports. Reading them is what git itself does to answer, and costs no
// process; on a loaded machine a git process is tens of milliseconds, and the
// lock alone was read five times per worktree per pass.
//
// Each reader answers only when the files say exactly what git would print,
// and otherwise reports !ok so the caller asks git: a main checkout or a
// submodule, a missing or unusual file, a lock reason git would quote, an
// environment that redirects git (GIT_DIR and friends), or a repository not
// owned by this user (which git refuses: safe.directory). Nothing here writes.

// gitEnvRedirects reports whether this process's environment would make git
// look somewhere other than the files read here.
func gitEnvRedirects() bool {
	for _, k := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM"} {
		if _, set := os.LookupEnv(k); set { //nolint:forbidigo // mirrors what the git child process would read; not configuration
			return true
		}
	}
	return false
}

// linkedAdminDir is what `git rev-parse --absolute-git-dir` prints for a
// linked worktree checkout: the directory its .git file names, symlinks
// resolved.
func linkedAdminDir(checkout string) (string, bool) {
	if gitEnvRedirects() || !isLinkedWorktree(checkout) {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(checkout, ".git"))
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(b))
	if !strings.HasPrefix(line, "gitdir:") || strings.Contains(line, "\n") {
		return "", false
	}
	dir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(checkout, dir)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || !isDir(real) || !ownedByMe(checkout) || !ownedByMe(real) {
		return "", false
	}
	return real, true
}

// linkedCommonDir is what `git rev-parse --path-format=absolute
// --git-common-dir` prints for a linked worktree checkout.
func linkedCommonDir(checkout string) (string, bool) {
	admin, ok := linkedAdminDir(checkout)
	if !ok {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(admin, "commondir"))
	if err != nil {
		return "", false
	}
	c := strings.TrimSpace(string(b))
	if c == "" || strings.Contains(c, "\n") {
		return "", false
	}
	if !filepath.IsAbs(c) {
		c = filepath.Join(admin, c)
	}
	real, err := filepath.EvalSymlinks(c)
	if err != nil || !isDir(real) || !ownedByMe(real) {
		return "", false
	}
	return real, true
}

// lockFromFiles is inspectLock's answer for a linked worktree, read the way
// `git worktree list --porcelain` reads it: every <common>/worktrees/<name>
// with a non-empty gitdir file is a registration at that path (its "/.git"
// stripped), locked when it has a `locked` file, whose content trimmed of
// surrounding whitespace is the reason.
func lockFromFiles(checkout string) (lockInfo, bool) {
	common, ok := linkedCommonDir(checkout)
	if !ok {
		return lockInfo{}, false
	}
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil {
		return lockInfo{}, false
	}
	info := lockInfo{CommonDir: common}
	for _, e := range entries {
		admin := filepath.Join(common, "worktrees", e.Name())
		b, err := os.ReadFile(filepath.Join(admin, "gitdir"))
		switch {
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid):
			continue // git skips an admin directory it cannot read a path from
		case err != nil:
			if fi, serr := os.Stat(admin); serr == nil && !fi.IsDir() {
				continue // a stray file beside the admin directories
			}
			return lockInfo{}, false
		}
		p := strings.TrimRight(string(b), " \t\n\v\f\r")
		if p == "" {
			continue
		}
		p = strings.TrimSuffix(p, "/.git")
		if !filepath.IsAbs(p) {
			p = filepath.Join(admin, p)
		}
		if !samePath(p, checkout) {
			continue
		}
		if info.Registered {
			return lockInfo{}, false // two registrations of one path: let git say which it lists
		}
		info.Registered = true
		reason, err := os.ReadFile(filepath.Join(admin, "locked"))
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return lockInfo{}, false
		default:
			r := strings.Trim(string(reason), " \t\n\v\f\r")
			if quotedByGit(r) {
				return lockInfo{}, false
			}
			info.Locked, info.Reason = true, r
		}
	}
	return info, true
}

// headFromFiles reads a linked worktree's HEAD from its admin directory: a
// symbolic ref ("ref: refs/heads/x"), or a detached commit id. A repository
// whose refs live in a reftable keeps only a placeholder there, so it, and
// anything else unexpected, is left to git.
func headFromFiles(admin string) (symbolic bool, sha string, ok bool) {
	common, err := os.ReadFile(filepath.Join(admin, "commondir"))
	if err != nil || gitEnvRedirects() {
		return false, "", false
	}
	c := strings.TrimSpace(string(common))
	if !filepath.IsAbs(c) {
		c = filepath.Join(admin, c)
	}
	if _, err := os.Lstat(filepath.Join(c, "reftable")); !errors.Is(err, fs.ErrNotExist) {
		return false, "", false
	}
	b, err := os.ReadFile(filepath.Join(admin, "HEAD"))
	if err != nil {
		return false, "", false
	}
	head := strings.TrimRight(string(b), "\n")
	switch {
	case strings.HasPrefix(head, "ref: refs/") && !strings.ContainsAny(strings.TrimPrefix(head, "ref: "), " \t\r\n"):
		return true, "", true
	case isObjectID(head):
		return false, head, true
	}
	return false, "", false
}

func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// quotedByGit reports whether git's porcelain output C-quotes s
// (quote_c_style with core.quotePath at its default): then the reason it
// prints is not the file's bytes, and only git's own answer is the same.
func quotedByGit(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == '"' || c == '\\' || c >= 0x7f {
			return true
		}
	}
	return false
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
