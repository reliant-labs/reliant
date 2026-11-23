package generator

import "golang.org/x/mod/semver"

// The Vite scaffolds' dev-server receiver for installDevLogging(): the
// runtime's forgeDevLog() once the floor forge scaffolds admits a runtime that
// ships it, the inlined src/lib/devlog-plugin.ts until then. Decided at render
// time from webRuntimePublishedRange, so it needs no network and no test that
// flips on its own: the release bookkeeping that raises the floor flips the
// scaffold in the same commit. Existing projects are untouched either way —
// vite.config.ts and devlog-plugin.ts are scaffold-once, and theirs.

// webRuntimeForgeDevLogSince is the first @reliantlabs/forge-web-runtime
// release that exports forgeDevLog from /vite.
//
// The release COMPUTES runtime versions (scripts/release-web-runtime.sh), so
// this is a prediction: forgeDevLog landed while the newest release was
// 0.13.19, and a non-breaking change is a patch bump, so the next cut that
// publishes from a commit containing it publishes 0.13.20. If that cut is
// taken from an older commit (a train pinning a forge commit from before
// forgeDevLog merged), 0.13.20 will lack it: raise this to the version that
// does ship it (check `dist/vite.d.ts` of the published tarball) before the
// floor reaches 0.13.20.
//
// That happened: v1.9.0's cut published 0.13.20 from a commit before
// forgeDevLog merged (#2417), and its dist/vite.d.ts exports no forgeDevLog.
// So the next publish, 0.13.21, is the first that can ship it.
const webRuntimeForgeDevLogSince = "0.13.21"

// inlinedDevLogPlugin is the scaffolded copy of the receiver, relative to the
// frontend root.
const inlinedDevLogPlugin = "src/lib/devlog-plugin.ts"

// viteDevLogFromRuntime reports whether a scaffold whose runtime floor is
// floorRange can import forgeDevLog: every version the range admits must ship
// it, so the range's MINIMUM is what is compared. A later minor or major
// keeps the export, so this is a plain ordering, not a caret match.
func viteDevLogFromRuntime(floorRange string) bool {
	floor, ok := minimumVersion(floorRange)
	if !ok {
		return false
	}
	since, ok := minimumVersion(webRuntimeForgeDevLogSince)
	return ok && semver.Compare(floor, since) >= 0
}
