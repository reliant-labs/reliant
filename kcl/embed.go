// Package kcl embeds the forge KCL module — the typed schemas +
// manifest render layer that generated projects' deploy/kcl env files
// import (`import forge`, `import forge.workloads`).
//
// Why the module rides inside the binary: dev builds of forge have no
// published `kcl-vX.Y.Z` git tag to resolve the scaffold's kcl.mod
// dependency against, exactly like a local or dirty build has no forge
// version any Go module proxy can serve. The Go-side answer is to pin
// nothing and demand a source bridge: generate's forge-compat handshake
// (internal/cli/generate_pkg_compat.go) refuses to generate against a
// published pin until a gitignored go.work `use`s a forge checkout
// (`forge project new --link-forge` writes one). The KCL side cannot ask
// for that — a daemon or `go install`'d binary may have no forge checkout
// on disk at all — so its answer is supplying THIS embedded copy to every
// KCL evaluation as an external package, from a content-addressed user
// cache (internal/kclvendor). For a released binary that copy IS the
// release's module.
//
// The embed deliberately covers only what a consuming project needs to
// resolve and render: kcl.mod plus the schema/render sources (root *.k,
// workloads/, lib/). tests/ and example/ are module-development
// artifacts and are excluded; kcl.mod.lock is derived state.
package kcl

import "embed"

// Module is the embedded forge KCL module, rooted at this directory
// (paths like "kcl.mod", "schema.k", "workloads/expand.k").
//
//go:embed kcl.mod *.k workloads/*.k lib/*.k tiers/*.k
var Module embed.FS
