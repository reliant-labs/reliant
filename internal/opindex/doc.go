// Package opindex is the static half of `forge obs blast-radius` and of
// op-scoped release health: it lists a project's ops from its source, and
// maps a diff to the ops the diff can change.
//
// An op is a unit the running code names a span after, so the static name
// and the telemetry join on span name alone:
//
//   - component ops: each method of an internal package's contract that
//     the generated observability decorator (middleware_gen.go) wraps. The
//     decorator passes the op name to the observe chain as a string literal,
//     `<pkg>.<Method>`, and [Build] reads that literal back rather than
//     re-deriving it. Span kind INTERNAL.
//   - rpc ops: each rpc of each service in a .proto under proto/, named as
//     otelconnect names its server span, `<proto package>.<Service>/<Method>`.
//     Span kind SERVER.
//
// Each op is located at its implementation: for a component, the method of
// that name on the package's concrete type (the type the constructor builds
// first, then the first in file order; the interface method when no concrete
// type implements it); for an rpc, the method on *Service in the handler
// package codegen.ResolveServiceComponent resolves for the service (the
// proto rpc line when there is none).
//
// # Mapping a diff to ops
//
// A diff is read as [FileChange]s ([Changes]: git's name-status plus -U0
// hunks). Every changed file ends up either in some op's [Reason]s or in
// [Mapping.Unattributed] with a reason, so nothing is dropped silently.
//
// Files are classified by path first:
//
//   - `*_gen.go`, `*.pb.go` and anything under gen/ are unattributed as
//     "generated": their inputs (a contract, a proto, a migration) are the
//     change to read.
//   - `*_test.go` is unattributed as "test file".
//   - a file that is neither Go nor proto is unattributed as "not Go source".
//
// A Go file is parsed on both sides of the diff. A top-level declaration —
// func, method, type, var or const — changed when its line span intersects a
// changed line on the head side (a point where lines were deleted counts
// when it is strictly inside the span), or a removed line on the base side;
// a declaration only the base side has was deleted, and counts too. Import
// blocks are not declarations: a change that touches only comments, imports
// or blank lines is unattributed as such. Each changed declaration is mapped
// within its own package, through that package's static reference graph:
//
//   - method: it is the implementing method of a component op.
//   - handler: it is an rpc's handler method, or a declaration in the
//     handler's own file (rpc_<name>.go) that the handler reaches.
//   - helper: an op's entry method reaches it through the graph. It is
//     attributed to EVERY op that reaches it.
//   - package: no entry method reaches it — a constructor, an init, a Deps
//     struct, a func only other packages call, or a declaration the working
//     tree no longer has. Every op of the package is named, since which of
//     them it affects cannot be read from the source.
//
// A Go file in a package with no ops is unattributed with "package <dir> has
// no ops". A file that does not parse names every op of its package
// (package, with no function).
//
// The graph is untyped (no go/types): an identifier naming a package-level
// declaration is an edge to it (calls, callbacks, type uses — a struct
// change reaches every method on it), and a selector `x.M` is an edge to
// every method named M in the package. It over-approximates, never under:
// a mapping may name an op the change cannot really reach, but not miss one
// reached through a same-package reference.
//
// A .proto file is scanned on both sides. A changed rpc declaration maps to
// its op (proto); a changed message maps to every rpc taking or returning it
// directly, in the same file or, by fully qualified name, anywhere under
// proto/ (proto). A message only nested inside other messages is
// unattributed, as is a change outside every rpc and message.
//
// # Ranges and commits
//
// [Index.MapRange] maps each non-merge commit of base..head on its own
// diff (against its first parent), so each [MappedOp] lists the commits
// that changed it, newest first, with the PR number a squash-merge subject
// ends with ([ParsePR]). With a head, the range is those commits alone,
// tree to tree: nothing uncommitted is read. Without one (head ""), it is
// the commits base..HEAD plus a pseudo-commit with SHA "" and subject
// "uncommitted changes": HEAD against the working tree, so staged and
// unstaged edits alike (where the index and the working tree disagree, the
// working tree's content is what counts), and every untracked file
// .gitignore does not exclude, as a whole-file addition. An untracked file
// that is not Go or proto source is unattributed like any other.
// Only the newest release.MaxRangeCommits commits are mapped, and
// [Mapping.Truncated] says when the range held more. [MergeBase] resolves a
// branch's fork point, the usual base.
//
// # Limitations
//
//   - The ops and call graphs are those of the working tree [Build] read. A
//     commit's changed declarations are looked up in it by name, so a
//     function renamed or deleted since that commit falls back to package.
//     An rpc the working tree no longer declares is still reported, from the
//     commit's own proto, with [MappedOp.Removed] set.
//   - Reach is never followed across packages: a change to a package with
//     no ops (internal/db, a util) is unattributed even when an op's package
//     calls it. Interface dispatch inside a package is matched by method
//     name only.
//   - Merge commits are not mapped in a range; the commits they merge are.
//
//forge:exclude-contract: a value type (the built Index) read by the CLI; its methods are lookups over data Build already computed, and git and the filesystem are the only collaborators, so there is nothing to substitute
package opindex
