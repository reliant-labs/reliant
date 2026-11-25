package buildinfo

import "os"

// ci.go — "is this an automated build agent?", which is a DIFFERENT question
// from IsDevBuild's "was this binary built from source?".
//
// The two coincide everywhere except CI, and that gap is a real defect we
// shipped. Forge's own CI builds the binary under test from the working tree
// (`go install ./cmd/forge`), because installing a tag would validate every PR
// against the last release. So IsDevBuild() is TRUE on a CI runner — correct
// for its own question, and wrong for every consumer that meant "a maintainer
// is iterating locally".
//
// The devlink is the consumer that got burned. It bridges a project's
// frontends to a sibling web-runtime checkout, which is right for a maintainer
// editing both at once and wrong for a machine scaffolding a throwaway project
// to check that a RELEASE behaves. On CI the bridge added web-runtime's own
// node_modules as a second resolution root, so tsc saw two copies of
// @connectrpc/connect and @bufbuild/protobuf and failed the scaffold with
// TS2322 "Type Transport is not assignable to type Transport".
//
// That failure is worth stating plainly, because it is the exact class of bug
// internal/webruntimepeers exists to PREVENT: peers pins one copy of every
// shared package, and the devlink shipping alongside it reintroduced a
// duplicate by a route the pins do not cover. The pins cannot fix this one —
// the second resolution root has no business existing in that scaffold at all,
// and teaching tsconfig to tolerate it would leave CI validating a resolution
// graph no user ever gets. A green check that means nothing is worse than the
// bug.

// ciEnvVars are the environment variables consulted by IsCI, in the order a
// reader should think about them.
//
// CI is the near-universal convention — GitHub Actions, GitLab, CircleCI,
// Travis, Buildkite, Jenkins' modern images and Netlify all set CI=true — so
// it carries the check on its own almost everywhere. GITHUB_ACTIONS is kept
// beside it because it is the provider forge's own workflows run on, and a
// runner that somehow lost CI should still be recognised as one.
var ciEnvVars = []string{"CI", "GITHUB_ACTIONS"}

// forceDevWebRuntimeLinkEnv opts a CI run BACK IN to the dev-loop artifacts
// that IsCI otherwise suppresses.
//
// The escape hatch is deliberate: the CI check is a default, not a wall. A
// maintainer debugging the bridge on a runner, or a pipeline that genuinely
// wants to exercise a sibling web-runtime checkout, must not be locked out by
// a heuristic they cannot override. Any non-empty value turns it back on.
const forceDevWebRuntimeLinkEnv = "FORGE_DEV_WEBRUNTIME_LINK"

// IsCI reports whether forge is running on an automated build agent.
//
// Callers use it to suppress artifacts that only make sense in a human's
// dev loop. It is deliberately NOT folded into IsDevBuild: that function
// answers a question about the BINARY (built from source vs. released), and
// conflating the two would make a CI build of a dev binary claim to be a
// release, changing version reporting and the scaffolded go.work bridge as a
// side effect of an unrelated fix.
func IsCI() bool {
	mu.RLock()
	ov, ovSet := ciOverride, ciOverrideSet
	mu.RUnlock()
	if ovSet {
		return ov
	}
	for _, name := range ciEnvVars {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}

// DevWebRuntimeLinkForced reports whether the operator has explicitly demanded
// the web-runtime dev link despite running under CI.
func DevWebRuntimeLinkForced() bool {
	return os.Getenv(forceDevWebRuntimeLinkEnv) != ""
}

// ciOverride / ciOverrideSet are a test seam mirroring devBuildOverride.
// Tests must be able to pin the answer without mutating process environment:
// the e2e corpus runs t.Parallel() and Go panics on t.Setenv in a parallel
// test, so reading env at the call site would be untestable there.
var (
	ciOverride    bool
	ciOverrideSet bool
)

// SetCI pins IsCI's answer. Test-only seam — pair with ClearCI in a
// t.Cleanup.
func SetCI(v bool) {
	mu.Lock()
	defer mu.Unlock()
	ciOverride = v
	ciOverrideSet = true
}

// ClearCI removes any override set by SetCI, restoring the environment read.
func ClearCI() {
	mu.Lock()
	defer mu.Unlock()
	ciOverride = false
	ciOverrideSet = false
}
