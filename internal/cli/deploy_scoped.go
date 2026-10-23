package cli

// A SCOPED deploy must name a release.
//
// THE BUG. `forge env deploy <env>` with no version does everything: build
// every artifact the env declares, push each one, cut a release over them,
// plan, confirm, promote, apply, wait (O-15, deploy_all.go). Adding a scope
// flag — `--frontends-only`, `--target api` — changed only the LAST step. So
// `forge env deploy prod --frontends-only`, which reads as "ship just the
// frontend", built and pushed every backend image, cut a full release, gated
// on it, and then applied the frontend alone. The caller paid for a whole
// build to ship one part of it, and in CI without --yes paid for it and then
// exited 5 having written nothing.
//
// WHY REFUSING IS THE FIX, AND NOT "BUILD ONLY THE SCOPED PART". The second
// option is the one that sounds better, and it is not available: it needs a
// release that records a SUBSET of the env's artifacts, and pkg/release does
// not model one. A Release's Artifacts map is the complete set the version
// names, and every consumer reads it that way — Promotion pins each artifact's
// digest, `forge env status` reports the bound release's artifacts as what the
// env runs, SameContent compares the whole map for the reuse lookup. Cutting a
// version whose map held only the frontends would make all three of those lie
// about what the env is running, which is a far worse failure than a refused
// command: the first is silent and durable, the second is one line on a
// terminal.
//
// So forge refuses and names the two things the caller might actually have
// meant. Both already work:
//
//   - ship the scoped part of a release that exists:
//     `forge env deploy prod v1.7.1 --frontends-only`
//   - ship this checkout, all of it: `forge env deploy prod`
//
// The versioned form is unaffected, and that is the point — it builds nothing,
// so a scope flag there costs nothing and means exactly what it says. The
// refusal is specifically about the combination that cannot be honest: no
// version (so a release must be cut) plus a scope (so the release would not
// describe what shipped).
//
// `--dry-run` and `--explain` never reach this: they write nothing and cut
// nothing, so a scoped preview is a sensible question with a correct answer.
// dispatchDeployCmd routes them to the apply-only path before this is called.
//
// NOT IN THE SET: --skip-frontend. It is the "deploy the whole backend" path,
// and a release cut from it describes every artifact the env declares — the
// build is the full build either way, so there is no scoped release to cut and
// nothing to misreport. Only the apply is narrowed, which is what that flag
// is for.

import (
	"errors"
	"strings"
)

// refuseScopedDeployWithoutRelease refuses a scope flag on a deploy that names
// no release, so forge never cuts a full release to ship part of it.
//
// It returns nil for an unscoped deploy, which is the whole of the common
// path.
func refuseScopedDeployWithoutRelease(envName string, f deployCmdFlags) error {
	scope, example := scopeOfDeploy(f)
	if scope == "" {
		return nil
	}
	return errors.New("a scoped deploy needs an existing release: " + scope + " ships only part of the env, " +
		"but a deploy with no version builds and pushes EVERY artifact and cuts a release over all of them.\n" +
		"  Ship the scoped part of a release that already exists:\n" +
		"    forge env deploy " + envName + " <version> " + example + "\n" +
		"  Or deploy everything at this checkout:\n" +
		"    forge env deploy " + envName + "\n" +
		"  (`forge env status " + envName + "` names the version the env runs now.)")
}

// scopeOfDeploy names the scope flag in play and the example spelling to show,
// or "" when the deploy is unscoped.
//
// --frontends-only is reported ahead of --target because the two are already
// mutually exclusive: a caller who set both gets that error, from the branch
// that owns it, rather than a second opinion from here.
func scopeOfDeploy(f deployCmdFlags) (scope, example string) {
	switch {
	case f.frontendsOnly:
		return "--frontends-only", "--frontends-only"
	case len(f.targets) > 0:
		return "--target " + strings.Join(f.targets, " --target "), "--target " + f.targets[0]
	default:
		return "", ""
	}
}
