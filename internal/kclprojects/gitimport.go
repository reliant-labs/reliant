package kclprojects

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/gitsource"
)

// locateGit resolves a git-pinned import through the shared source resolver:
// the same cache, keyed by repo and ref, and the same local-override file as
// every other forge.GitSource, so there is one answer to "where is that repo
// at that ref" on this machine.
//
// The whole repository is resolved (no subdir), so ModuleDir is the checkout
// root — the boundary the forge.yaml search stops at — and Dir is the KCL
// package inside it.
func (r *resolver) locateGit(imp Import) (Resolved, error) {
	if r.sources == nil {
		sr, err := gitsource.NewResolver(r.projectDir)
		if err != nil {
			return Resolved{}, fmt.Errorf("%s [%s] %s: %w", imp.DeclaredIn, TableName, imp.Name, err)
		}
		r.sources = sr
	}
	src := gitsource.Source{Repo: imp.Repo, Ref: imp.Ref}
	if r.refresh {
		if err := r.sources.Refresh(src); err != nil {
			return Resolved{}, fmt.Errorf("%s [%s] %s: drop the cached %s: %w", imp.DeclaredIn, TableName, imp.Name, src, err)
		}
	}
	res, err := r.sources.Resolve(r.ctx, src)
	if err != nil {
		return Resolved{}, fmt.Errorf("%s imports forge project %q pinned to %s, which could not be resolved: %w\n"+
			"  A git-pinned import is read from the machine-local source cache and fetched only on a miss, so a render\n"+
			"  offline works once the pin has been fetched here. To get past this:\n"+
			"    - online: check the repo and ref exist and that git can reach the repo (credentials, ssh keys), then\n"+
			"      fetch it with `forge kcl imports`;\n"+
			"    - developing the imported repo: map it to your working copy in %s:\n"+
			"        sources:\n"+
			"          %s: ../<checkout>",
			imp.DeclaredIn, imp.Name, imp.Pin(), err,
			filepath.Join(gitsource.OverridesDirName, gitsource.OverridesFileName), imp.Repo)
	}

	out := Resolved{Import: imp, ModuleDir: res.Dir, Overridden: res.Overridden}
	out.Dir = filepath.Join(res.Dir, filepath.FromSlash(imp.Subdir))
	switch {
	case res.Overridden:
		// An override is the developer saying "read my working copy", so the
		// commit lock is not judged against it — but it is never silent.
		out.Via = "local override in " + filepath.Join(gitsource.OverridesDirName, gitsource.OverridesFileName) + " — NOT the pinned ref"
		r.log(fmt.Sprintf("forge: KCL import %q: %s → %s (local override — NOT the pinned ref)", imp.Name, imp.Pin(), res.Dir))
	default:
		out.ResolvedCommit = res.Commit
		out.Version = imp.Ref
		out.Via = "kcl.mod git pin"
		if res.Commit != "" {
			out.Via += " at " + res.Commit
		}
		if err := checkCommitLock(imp, res); err != nil {
			return Resolved{}, err
		}
		if !res.Cached {
			r.log(fmt.Sprintf("forge: KCL import %q: fetched %s (%s)", imp.Name, imp.Pin(), shortCommit(res.Commit)))
		}
	}
	return out, nil
}

// checkCommitLock refuses a git pin whose ref resolved to a commit other
// than the one the import declares (`commit`), or — for a ref that is itself
// a full sha — other than that sha. Without it, a tag that moved, or a branch
// that advanced before this machine's first fetch, renders different KCL on
// different machines from one kcl.mod.
func checkCommitLock(imp Import, res gitsource.Resolution) error {
	want, what := imp.Commit, "commit"
	if want == "" && fullSHA(imp.Ref) {
		want, what = imp.Ref, "ref"
	}
	if want == "" {
		return nil
	}
	if res.Commit == "" {
		return fmt.Errorf("%s [%s] %s pins %s, but the resolved checkout records no commit, so the %s lock cannot be verified.\n"+
			"  Drop the cache entry and fetch it again: forge kcl imports --refresh",
			imp.DeclaredIn, TableName, imp.Name, imp.Pin(), what)
	}
	if strings.HasPrefix(res.Commit, want) {
		return nil
	}
	return fmt.Errorf("%s [%s] %s: pin mismatch — %s resolved to commit %s, but the import declares %s %s.\n"+
		"  The ref moved (a re-pointed tag, an advanced branch), or this machine's cached copy of it is older than the lock.\n"+
		"  If %s is the commit you mean to render, refetch the ref: forge kcl imports --refresh.\n"+
		"  If the ref moved on purpose, review the change and set %s = %q in %s",
		imp.DeclaredIn, TableName, imp.Name, imp.Repo+"@"+imp.Ref, res.Commit, what, want, want, what, res.Commit, imp.DeclaredIn)
}

func fullSHA(s string) bool {
	return len(s) == 40 && commitRE.MatchString(s)
}

func shortCommit(c string) string {
	if c == "" {
		return "commit unknown"
	}
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
