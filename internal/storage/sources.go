package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/gitsource"
)

// SourceCacheRoot overrides the cross-repo source cache this Runner
// maintains. Empty means the real machine-local cache
// (<UserCacheDir>/forge/sources), which is what production wants.
//
// It exists because this layer is the only one in GC that reclaims from a
// fixed ABSOLUTE path outside the project: Logs walks Policy.Projects
// (empty by default, so inert), and the registry layers go through an
// injected Command. A test that merely wanted to assert "GC rejects a
// remote Docker endpoint" would otherwise delete a developer's real
// clones as a side effect — which it did, once, before this field
// existed. A reclaimer whose blast radius is not addressable from the
// test that calls it is a reclaimer that will eventually be run by
// accident.
// Under `go test` an unset root is REFUSED rather than defaulted. Opting
// out by remembering to set a field is not a safeguard — the four tests
// that deleted real clones were asserting things about Docker endpoints
// and had no reason to think about a source cache at all. Making the
// default unreachable from a test turns "remember this" into a compile-
// time-adjacent failure with a message that says what to set.
func (r Runner) sourceCacheRoot() (string, error) {
	if r.SourceCacheRoot != "" {
		return r.SourceCacheRoot, nil
	}
	if testing.Testing() {
		return "", fmt.Errorf("storage.Runner.SourceCacheRoot is unset under test: " +
			"set it to a t.TempDir() (an unset root would reclaim the developer's real ~/.cache/forge/sources)")
	}
	return gitsource.DefaultCacheRoot()
}

// Sources reclaims cross-repo source clones that no project has resolved
// recently. One full clone exists per repo+ref pin and nothing else ever
// removes one, so a project that bumps its pin weekly accumulates a
// complete checkout per bump — 9.2 GB of one repository on the machine
// this layer was written for, most of it an installed node_modules.
//
// The eviction MECHANISM and its safety rules live in internal/gitsource,
// next to the code that writes the cache; the BUDGETS come from this
// machine's storage policy, the way every other layer's do, so a developer
// tunes one file rather than learning where each reclaimer hides its
// defaults. The newest-K floor means an idle project's current pin
// survives, so this is safe to run unattended.
//
// An unparseable SourceCacheUnused cannot happen on a loaded policy —
// Validate rejects it and GC calls Validate first — so it is treated the
// same as an unset value: the gitsource default, never "evict everything".
//
// A cache that cannot be located is not a failure: os.UserCacheDir fails
// on a host with no HOME, where there is also nothing to reclaim.
func (r Runner) Sources(apply bool) error {
	root, err := r.sourceCacheRoot()
	if err != nil {
		r.print("skip source cache (%v)\n", err)
		return nil
	}
	maxAge, parseErr := time.ParseDuration(r.Policy.SourceCacheUnused)
	if parseErr != nil {
		maxAge = 0
	}
	ctx := r.hostCtx()
	del := r.deletes()
	policy := gitsource.EvictPolicy{
		MaxAge: maxAge, KeepPerSlug: r.Policy.SourceCacheKeep, Ctx: ctx,
		// A clone is a whole tree — an installed node_modules can be 100k
		// files — so it is unlinked through the pass's governor like every
		// other cache, not by an unpaced RemoveAll.
		RemoveTree: func(path string) error { return del.removeTree(ctx, path) },
	}
	if r.openShared != nil || r.OpenPaths != nil {
		policy.InUse = r.sharedInUse(ctx)
	}
	got, err := gitsource.Evict(root, time.Now(), policy, apply, r.Out)
	if err != nil {
		if fsDistress(err) {
			err = del.observe(root, err) // a rename or read, not one of del's own unlinks
		}
		if stopsPass(err) {
			r.print("source cache: stopped (%v)\n", err)
		}
		return err
	}
	if len(got.Removed) > 0 {
		r.print("source cache: %d entries, %.1f GiB (%d retained)\n",
			len(got.Removed), float64(got.Bytes)/float64(GiB), got.Kept)
	}
	return nil
}
