// Package codegen — geninput.go defines GenContext, the shared
// project-scoped context that every emitter needs (where the project
// lives, its module path, and the checksum tracker that keeps Tier-1
// output recorded in .forge/hashes.json).
//
// Historically the core emitters threaded ProjectDir / ModulePath /
// *checksums.FileChecksums through 7-12 positional parameters each.
// Newer emitters (ingress_k3d_gen) adopted a per-emitter
// GenInput struct instead. GenContext finishes that
// migration: GenInput structs EMBED it so the three fields are declared
// once and accessed uniformly (in.ProjectDir, in.ModulePath,
// in.Checksums) via Go's field promotion, while the per-emitter struct
// adds only the fields that emitter actually varies on.
//
// why embed rather than a *GenContext field: promotion keeps every
// existing `in.ProjectDir` / `in.Checksums` reference inside an emitter
// compiling unchanged, so converting an emitter to the struct form is a
// signature change at the call site only — the body is untouched.
package codegen

import "github.com/reliant-labs/forge/internal/checksums"

// GenContext is the project-scoped context shared by every emitter's
// GenInput. Embed it; do not pass it around on its own.
type GenContext struct {
	// ProjectDir is the project root. Output paths are computed relative
	// to it, and it's the base for the relative paths recorded in
	// Checksums. Some env-scoped emitters tolerate an empty ProjectDir
	// when their output lives outside the project tree (see the emitter's
	// own doc); the core bootstrap emitters require it.
	ProjectDir string

	// ModulePath is the Go module path (e.g.
	// "github.com/acme/control-plane"), used to build import lines and
	// derive resource names (leader-election lease, etc.). Empty for
	// emitters that don't render Go imports (k3d ports, deploy config).
	ModulePath string

	// Checksums, when non-nil, records each rendered Tier-1 file's hash
	// so `forge project audit` doesn't flag forge-owned output as stale. A nil
	// tracker is tolerated everywhere — the file is still written.
	Checksums *checksums.FileChecksums
}
