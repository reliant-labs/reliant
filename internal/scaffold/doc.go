// Package scaffold holds forge's BIRTH-TIME, SCAFFOLD-ONCE emitters:
// code that renders user-owned artifacts exactly once, at an explicit
// `forge scaffold ...` moment, and never touches them again.
//
// The package exists as an architectural statement, splitting the two
// writer families that used to share internal/codegen:
//
//   - internal/codegen — Tier-1 DETERMINISTIC PROJECTION. Same inputs,
//     same bytes, regenerated on every `forge generate`, stamped with
//     forge:hash, drift-guarded. Forge owns those files forever.
//
//   - internal/scaffold (this package) — SCAFFOLD-ONCE. Rendered from a
//     truth (the proto descriptor, the applied schema) at an explicit
//     add-time decision, written through checksums.WriteScaffoldIfMissing
//     or a minimal idempotent splice, and USER-OWNED from birth. Deleting
//     the file is the reset; no generate pass ever rewrites it.
//
// Nothing in this package may be called from the generate pipeline: a
// scaffold-once artifact that regenerates is a contradiction in terms.
// Conversely, Tier-1 emitters (crud_gen, inject_gen, the ORM, ...) stay
// in internal/codegen — a projection that stops regenerating is equally
// wrong. When a helper is genuinely shared (descriptor types, the
// unwired-stub marker, handler-method scans), it lives in codegen and is
// imported from here; the dependency is one-directional
// (scaffold → codegen), never the reverse.
//
// Current residents:
//
//   - entityproto.go — the create-table migration born from an
//     already-authored proto message (`forge scaffold entity
//     --from-proto`), user-owned at birth and never re-derived.
package scaffold
