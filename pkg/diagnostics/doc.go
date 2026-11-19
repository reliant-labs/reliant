// Package diagnostics surfaces unwired forge scaffolds at runtime.
//
// # Why this exists
//
// Forge scaffolds Tier-1 stubs that compile cleanly but return
// `connect.CodeUnimplemented` / `ErrNotImplemented`, and wire_gen.go
// constructs Deps fields as typed-zero values when no producer matched
// (see `internal/codegen/wire_gen.go` for the producer-resolution
// rules). Both are legitimate during a migration: the user can ship a
// scaffold that hasn't been filled in yet and still have a passing
// build. Both also caused a real production outage (kalshi-trader,
// 2026-06-03) when an operator was unaware that the on-disk YAML
// config wasn't being loaded — the stubbed loader silently returned
// `ErrNotImplemented`, the caller fell back to a Go-literal default
// with looser knobs, and a cron worker no-op'd for ~24h before anyone
// noticed.
//
// `forge lint --wire-coverage` catches the nil-dep half at develop
// time and `forge lint --scaffolds` catches `FORGE_SCAFFOLD:` markers
// at commit time. Neither is visible to the operator watching boot
// logs in production, and this package is the runtime third leg of
// that stool.
//
// # Status: a library, not yet a pipeline
//
// What is HERE and works: the Diagnostic shape, a Registry to collect
// diagnostics and Boot them in a stable order, and the emitters
// (LogEmitter / NopEmitter / MultiEmitter, plus StrictEmitter to turn
// any of them fatal). A caller that registers diagnostics itself and
// calls Boot gets exactly the described log lines.
//
// What is NOT here: anything that FILLS the registry automatically.
// Nothing in forge imports this package. In particular there is no
// codegen step emitting a `pkg/app/diagnostics_gen.go` whose `init()`
// registers the scaffolds it found, and no Bootstrap call to
// `Default().Boot`. So a project depending on forge today gets no
// boot-time warning about its unwired scaffolds — the registry is
// simply empty, and an empty registry emits nothing.
//
// The `features.experimental.strict_wiring` flag exists in
// `internal/config` and is accepted in forge.yaml, but nothing reads
// it to construct a StrictEmitter; a project setting it changes no
// behaviour. `StrictEmitter` is usable directly by a caller that wraps
// its own emitter.
//
// Closing the gap means the producer side: detecting stubs and nil
// Deps at generate time and emitting the registration file, then
// calling Boot from the generated bootstrap. Until then, treat this
// package as a library with no callers rather than as a subsystem
// that is running.
package diagnostics
