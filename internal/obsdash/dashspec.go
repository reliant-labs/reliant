// The dashboard spec vocabulary and its validator live in pkg/dashspec, so a
// module that reads forge's dashboards (the control plane, which saves and
// promotes them) validates with forge's own rules instead of a copy. obsdash
// builds and renders dashboards in that vocabulary; these aliases keep its
// code, and its callers, unqualified.

package obsdash

import "github.com/reliant-labs/forge/pkg/dashspec"

type (
	// Signal is dashspec.Signal.
	Signal = dashspec.Signal
	// PanelKind is dashspec.PanelKind.
	PanelKind = dashspec.PanelKind
	// FunnelBy is dashspec.FunnelBy.
	FunnelBy = dashspec.FunnelBy
	// Funnel is dashspec.Funnel.
	Funnel = dashspec.Funnel
	// Thresholds is dashspec.Thresholds.
	Thresholds = dashspec.Thresholds
	// Agg is dashspec.Agg.
	Agg = dashspec.Agg
	// Unit is dashspec.Unit.
	Unit = dashspec.Unit
	// Op is dashspec.Op.
	Op = dashspec.Op
	// Predicate is dashspec.Predicate.
	Predicate = dashspec.Predicate
	// Series is dashspec.Series.
	Series = dashspec.Series
	// Panel is dashspec.Panel.
	Panel = dashspec.Panel
	// Filter is dashspec.Filter.
	Filter = dashspec.Filter
	// AnnotationSource is dashspec.AnnotationSource.
	AnnotationSource = dashspec.AnnotationSource
	// Annotation is dashspec.Annotation.
	Annotation = dashspec.Annotation
	// Dashboard is dashspec.Dashboard.
	Dashboard = dashspec.Dashboard
	// Problem is dashspec.Problem.
	Problem = dashspec.Problem
)

// The spec constants, from pkg/dashspec.
const (
	Traces              = dashspec.Traces
	Metrics             = dashspec.Metrics
	Logs                = dashspec.Logs
	KindNumber          = dashspec.KindNumber
	KindLine            = dashspec.KindLine
	KindTable           = dashspec.KindTable
	KindSearch          = dashspec.KindSearch
	KindBar             = dashspec.KindBar
	KindHeatmap         = dashspec.KindHeatmap
	KindSQL             = dashspec.KindSQL
	KindIssues          = dashspec.KindIssues
	KindSessions        = dashspec.KindSessions
	KindFunnel          = dashspec.KindFunnel
	FunnelBySession     = dashspec.FunnelBySession
	FunnelByUser        = dashspec.FunnelByUser
	DefaultFunnelWindow = dashspec.DefaultFunnelWindow
	DefaultFunnelBy     = dashspec.DefaultFunnelBy
	AggCount            = dashspec.AggCount
	AggCountDistinct    = dashspec.AggCountDistinct
	AggSum              = dashspec.AggSum
	AggAvg              = dashspec.AggAvg
	AggMax              = dashspec.AggMax
	AggQuantile         = dashspec.AggQuantile
	AggLast             = dashspec.AggLast
	UnitNone            = dashspec.UnitNone
	UnitCount           = dashspec.UnitCount
	UnitMillis          = dashspec.UnitMillis
	UnitBytes           = dashspec.UnitBytes
	UnitPercent         = dashspec.UnitPercent
	UnitSeconds         = dashspec.UnitSeconds
	OpEq                = dashspec.OpEq
	OpNeq               = dashspec.OpNeq
	OpIn                = dashspec.OpIn
	OpNotIn             = dashspec.OpNotIn
	OpPrefix            = dashspec.OpPrefix
	OpMatch             = dashspec.OpMatch
	OpExists            = dashspec.OpExists
	OpGTE               = dashspec.OpGTE
	FieldService        = dashspec.FieldService
	FieldEnv            = dashspec.FieldEnv
	AnnotationReleases  = dashspec.AnnotationReleases
	SpecVersion         = dashspec.SpecVersion
)

// IsConsoleKind is dashspec.IsConsoleKind.
func IsConsoleKind(k PanelKind) bool { return dashspec.IsConsoleKind(k) }

// ParseDashboard is dashspec.ParseDashboard.
func ParseDashboard(name string, raw []byte) (Dashboard, error) {
	return dashspec.ParseDashboard(name, raw)
}

// ProblemsOf is dashspec.ProblemsOf.
func ProblemsOf(err error) []Problem { return dashspec.ProblemsOf(err) }

// LooksLikeSpecDashboard is dashspec.LooksLikeSpecDashboard.
func LooksLikeSpecDashboard(raw []byte) bool { return dashspec.LooksLikeSpecDashboard(raw) }

// LooksLikeHyperDXDashboard is dashspec.LooksLikeHyperDXDashboard.
func LooksLikeHyperDXDashboard(raw []byte) bool { return dashspec.LooksLikeHyperDXDashboard(raw) }
