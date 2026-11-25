package templates

import (
	"strings"

	"golang.org/x/mod/semver"
)

// forgeAtLeast reports whether pinned — a project's forge_version — is a
// forge that has everything release has.
//
// A template that RUNS a forge command newer than the release a checkout
// build pins (buildinfo.PublishedFloor) renders it only inside
// `{{if .ForgeAtLeast "vX.Y.Z"}}`, naming the first release that has the
// command, so a project pinned to an older forge keeps a form its forge can
// run. TestScaffoldTemplatesRunOnlyReleasedForgeCommands (internal/cli)
// accepts a command it cannot find in the released snapshot only inside such
// a guard.
//
// The comparison is on the core version: a pre-release of a release (an rc,
// or the Go pseudo-version of a commit after its tag, vX.Y.(Z+1)-0.…) has
// what that release has. A pin that is no version at all ("", a development
// build) has nothing past it: the guarded form is not rendered, which is the
// safe direction.
func forgeAtLeast(pinned, release string) bool {
	v := strings.TrimSpace(pinned)
	if !semver.IsValid(v) || !semver.IsValid(release) {
		return false
	}
	core, _, _ := strings.Cut(semver.Canonical(v), "-")
	return semver.Compare(core, release) >= 0
}
