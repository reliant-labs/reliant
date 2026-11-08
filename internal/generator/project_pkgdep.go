// Package generator — forge dependency resolution for scaffolded go.mod files.
//
// Generated projects import github.com/reliant-labs/forge/pkg/* (serverkit,
// appkit, orm, ...). Those are packages inside the SINGLE forge module —
// forge/pkg stopped being a companion submodule — so a scaffold pins ONE
// requirement, `require github.com/reliant-labs/forge vX.Y.Z`, in BOTH go.mod
// and gen/go.mod, with no replace and no vendoring; `go mod tidy` resolves it
// from the module proxy like any other dependency.
//
// WHICH VERSION.
//
//   - A binary a module proxy can serve (a release tag, or the clean
//     pseudo-version `go install .../cmd/forge@<ref>` records) pins ITSELF:
//     the version that generated the code and the one it compiles against are
//     the same number.
//   - A binary no proxy can serve (a local `go build`, any dirty tree) pins
//     the newest RELEASE its source descends from — buildinfo.PublishedFloor —
//     and the scaffold bridges it to this checkout with go.work
//     (writeDevForgeGoWork in internal/cli/new.go), which decides what the
//     build actually compiles.
//
// THE REQUIRE MUST NEVER BE EMPTY. It used to be for the second case, on the
// theory that pinning nothing is safer than pinning a version the generated
// code might outrun. It is not: `go mod tidy` ignores go.work, so a module
// importing forge/pkg/* with no forge requirement makes tidy search the proxy
// for a module providing that import path, and the longest match is the
// RETIRED github.com/reliant-labs/forge/pkg module. tidy wrote `forge/pkg
// v0.1.15` into both go.mod files, the next `forge generate` refused the
// project, and `forge env up` — which runs `go mod tidy -diff` — reported "go
// module graph is stale" on every start, because gen/'s tidy was never clean.
//
// The floor cannot reintroduce the old `undefined: testkit.StubNotConfigured`
// failure silently: with the bridge in place it compiles against source, and
// without one generate's pre-codegen check (internal/cli/generate_pkg_compat.go)
// refuses an unreleasable binary generating into a published pin before any
// file is touched.
package generator

import "github.com/reliant-labs/forge/internal/buildinfo"

// resolveForgeVersion returns the forge version a scaffolded go.mod requires:
// this binary's own version when a proxy can serve it, otherwise the newest
// published release its source descends from. It returns "" only when even
// that floor is unknown (a corrupted embedded VERSION file).
func resolveForgeVersion() string {
	if v := buildinfo.InstallableVersion(); v != "" {
		return v
	}
	return buildinfo.PublishedFloor()
}
