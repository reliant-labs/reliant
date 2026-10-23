package diagnostics

// diagnostics.go — public type surface.
//
// The two value types — Diagnostic and the Kind/Severity enums — are
// JSON-marshalable so `forge project audit --json` can roll them up under a
// new audit category without forcing a separate wire format. Field
// names match the canonical lint diagnostic shape from
// `internal/linter/forgeconv/forgeconv.go::Finding` (the canonical
// lint-finding shape, shared with internal/contractcheck) so
// dashboards that already consume forge JSON don't need new column
// mappings.

// Kind classifies an unwired scaffold by detection rule.
//
// Two kinds today, matching the two backlog rules (FORGE_BACKLOG.md
// 2026-06-03 entry):
//
//   - KindStubImpl: a generated function body whose only statement is
//     `return ..., ErrNotImplemented` (or a configurable sentinel
//     error). Detected at codegen time via the `// forge:gen
//     unwired-stub` marker emitted by the handler template.
//
//   - KindNilDep: a wire_gen.go DI site where a Deps field is
//     constructed as `nil` (or typed zero) with no `forge:stub-ok`
//     opt-out. Detected at codegen time by `internal/codegen/wire_gen.go`'s
//     UnresolvedFields tracking.
//
// Additional kinds can land additively without breaking existing
// consumers — the JSON tag and the audit-category contract both allow
// unknown enum values.
type Kind string

const (
	// KindStubImpl is a Tier-1 stub whose body is the
	// ErrNotImplemented sentinel only. The user is expected to fill
	// in the body and remove the FORGE_SCAFFOLD: marker; until they
	// do, every boot emits one diagnostic per occurrence.
	KindStubImpl Kind = "stub-impl"

	// KindNilDep is a wire_gen.go Deps field constructed as nil with
	// no acknowledged-stub opt-out. The user should either extend
	// *App with a matching field and assign it in setup.go, or mark
	// the Deps field `// forge:stub-ok reason=...`.
	KindNilDep Kind = "nil-dep"
)

// Severity classifies a diagnostic by emit policy.
//
// The default emitter writes warn-level slog lines. StrictEmitter
// upgrades the summary into a fatal exit when any diagnostic was
// emitted. The Severity field on Diagnostic itself is informational
// — it tells consumers what level the runtime emitter is going to use
// — and is not used to dispatch.
type Severity string

const (
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// Diagnostic is one registered unwired-scaffold record.
//
// Construction is private to the Registry's Register* methods; do not
// build Diagnostic literals from user code. The JSON shape is part of
// the forge project audit / lint contract.
type Diagnostic struct {
	// Kind is one of the KindXxx constants above.
	Kind Kind `json:"kind"`

	// Symbol is the canonical Go package.identifier the diagnostic
	// names. For KindStubImpl this is "<pkg>.<Func>" (e.g.
	// "botconfig.LoadFromYAML"). For KindNilDep it's
	// "<wireFunc>.<DepField>" (e.g.
	// "wireWorkerCalibratorRefitDeps.PgUnsettled").
	Symbol string `json:"symbol"`

	// File is the project-relative path of the codegen-emitted source
	// (forward slashes regardless of OS). Matches the path convention
	// used by `forge project audit --json` and `forge lint`.
	File string `json:"file"`

	// Line is the 1-indexed line number of the marker in File.
	Line int `json:"line"`

	// Component is the enclosing wire_gen function name for
	// KindNilDep diagnostics (e.g.
	// "wireWorkerCalibratorRefitDeps"). Empty for KindStubImpl —
	// stubs have no wiring component.
	Component string `json:"component,omitempty"`

	// DepName is the Deps field name for KindNilDep diagnostics
	// (e.g. "PgUnsettled"). Empty for KindStubImpl.
	DepName string `json:"dep_name,omitempty"`

	// Message is the one-line human-readable summary the LogEmitter
	// writes as the slog log message. Stable across regenerates so
	// log search queries don't break.
	Message string `json:"message"`

	// Severity is the planned emit severity. The LogEmitter ignores
	// this field (it always writes warn); StrictEmitter only triggers
	// a fatal exit when at least one diagnostic was emitted, not on
	// per-diagnostic severity. The field is informational for
	// audit-JSON consumers.
	Severity Severity `json:"severity"`
}
