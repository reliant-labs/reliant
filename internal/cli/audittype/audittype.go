// Package audittype holds the small, neutral value types shared by the
// `forge project audit` command group (internal/cli/audit) and the internal/cli
// code that contributes audit categories it cannot compute without
// package-cli internals (the KCL-entity-typed ingress / external-builds
// categories, and friction.go's auditFriction). It is a leaf package so
// both the group and internal/cli can depend on it without an import cycle
// (internal/cli blank-imports the groups).
//
// Only the per-category roll-up value lives here; the report assembly,
// the category collectors, and the JSON shape stay in internal/cli/audit.
package audittype

// Status is the per-category roll-up. The wire enum is kept tiny so the
// JSON shape is easy to grep / jq against.
type Status string

// Status enum values.
const (
	StatusOK    Status = "ok"
	StatusWarn  Status = "warn"
	StatusError Status = "error"
)

// Category is one section of the audit report. The shape mirrors the
// "category, status, summary, details" scheme — kept deliberately simple so
// a sub-agent can pluck `.summary` for a human-readable snippet or
// `.details` for structured fix-up data.
type Category struct {
	Status  Status         `json:"status"`
	Summary string         `json:"summary"`
	Details map[string]any `json:"details,omitempty"`
}
