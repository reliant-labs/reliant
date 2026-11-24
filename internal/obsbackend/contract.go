package obsbackend

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Backend is what a `forge obs` verb reads telemetry through. The local
// backend (the project's ClickStack) is the first implementation; the cloud
// backend (the control plane's ObservabilityService) is the second. Both
// compile with pkg/obsquery.
//
//forge:contract
type Backend interface {
	Info() Info
	Logs(ctx context.Context, req LogsRequest) (LogsResult, error)
	Traces(ctx context.Context, req TracesRequest) (TracesResult, error)
	Trace(ctx context.Context, req TraceRequest) (TraceResult, error)
	Endpoints(ctx context.Context, req EndpointsRequest) (EndpointsResult, error)
	MetricsList(ctx context.Context, req MetricsListRequest) (MetricsListResult, error)
	MetricsQuery(ctx context.Context, req MetricsQueryRequest) (MetricsQueryResult, error)
	MetricsDescribe(ctx context.Context, req MetricsDescribeRequest) (MetricsDescribeResult, error)
	MetricsExemplars(ctx context.Context, req MetricsExemplarsRequest) (MetricsExemplarsResult, error)
	Query(ctx context.Context, req QueryRequest) (QueryResult, error)
}

// IssueReader is what `forge obs issues|issue` read and write through. Both
// backends implement it. The local one groups ERROR logs at query time and
// keeps no state, so its UpdateIssueStatus is always a refusal; the cloud one
// is the control plane's IssuesService.
//
//forge:contract
type IssueReader interface {
	ListIssues(ctx context.Context, req ListIssuesRequest) (ListIssuesResult, error)
	GetIssue(ctx context.Context, req GetIssueRequest) (GetIssueResult, error)
	ListIssueEvents(ctx context.Context, req ListIssueEventsRequest) (ListIssueEventsResult, error)
	UpdateIssueStatus(ctx context.Context, req UpdateIssueStatusRequest) (UpdateIssueStatusResult, error)
	ListIssueTimeline(ctx context.Context, req ListIssueTimelineRequest) (ListIssueTimelineResult, error)
	AddIssueComment(ctx context.Context, req AddIssueCommentRequest) (AddIssueCommentResult, error)
	AssignIssue(ctx context.Context, req AssignIssueRequest) (AssignIssueResult, error)
}

// SessionReader reads browser sessions: the list, and one session's summary
// with its timeline (`forge obs sessions|session`). Both backends implement
// it. A local ClickStack has no browser tables, so its reads answer "could not
// look"; the cloud one is the control plane's RumService.
//
//forge:contract
type SessionReader interface {
	ListSessions(ctx context.Context, req ListSessionsRequest) (ListSessionsResult, error)
	GetSession(ctx context.Context, req GetSessionRequest) (GetSessionResult, error)
}

// UserActivityReader counts an env's users: active, new and returning over
// the range and the one before it, and per UTC day (`forge obs users`). Both
// backends compile pkg/obsquery's user-activity statement: the local one
// over its ClickStack, which has no product_events table and so answers
// "could not look"; the cloud one is the control plane's GetUserActivity.
//
//forge:contract
type UserActivityReader interface {
	UserActivity(ctx context.Context, req UserActivityRequest) (UserActivityResult, error)
}

// TelemetryArrivalReader reads whether the systems an env declares
// forge.Telemetry for are reaching the store: per signal the newest row, per
// scraped endpoint the newest result (`forge obs telemetry`). Only the local
// backend implements it.
type TelemetryArrivalReader interface {
	TelemetryArrival(ctx context.Context, req TelemetryArrivalRequest) (TelemetryArrivalResult, error)
}

// HealthReader judges a release: the windows before and after a promotion,
// per service, and what the release opened (`forge obs check`). The local
// backend judges from the ClickStack with pkg/releasehealth; the cloud
// backend asks the control plane's GetReleaseHealth, which applies the same
// rule.
//
//forge:contract
type HealthReader interface {
	ReleaseHealth(ctx context.Context, req HealthRequest) (HealthResult, error)
}

// ChangesReader lists what changed in an env: promotions, failed deploys and
// activity (`forge obs changes`).
//
//forge:contract
type ChangesReader interface {
	Changes(ctx context.Context, req ChangesRequest) (ChangesResult, error)
}

// OverviewReader reads the per-service request figures of a range: the
// first section of `forge obs status`. Both backends implement it; they
// compile with the same pkg/obsquery overview.
//
//forge:contract
type OverviewReader interface {
	Overview(ctx context.Context, req OverviewRequest) (OverviewResult, error)
}

// OpsReader ranks the env's ops — rpcs, endpoints and component methods —
// by RED figures (`forge obs ops`), and reads them for named ops over any
// window (release health, blast radius). Both backends implement it with the
// same pkg/obsquery panels; the cloud one runs them through QueryPanel.
//
//forge:contract
type OpsReader interface {
	Ops(ctx context.Context, req OpsRequest) (OpsResult, error)
}

// AlertReader lists the env's active alerts, firing or pending. Only the
// cloud backend implements it: locally the alert state is the `alerts`
// workload's own (`forge obs alerts check` evaluates the rules once).
//
//forge:contract
type AlertReader interface {
	Alerts(ctx context.Context) ([]Alert, error)
}

// AlertHistoryReader lists the env's alert state transitions since a time,
// newest first: what `forge obs check` reads to tell an alert that was
// already flapping before a promotion from one the release opened
// (pkg/releasehealth alert_baseline.go). Only the cloud backend implements
// it: a local stack keeps no alert history.
//
//forge:contract
type AlertHistoryReader interface {
	AlertHistory(ctx context.Context, since time.Time) ([]AlertTransition, error)
}

// DeliveryReader reads whether the control plane has delivered a working
// telemetry token into the clusters that ship the env's telemetry. Only the
// cloud backend implements it: a local stack needs no token.
//
//forge:contract
type DeliveryReader interface {
	TelemetryDeliveries(ctx context.Context) (DeliveriesResult, error)
}

// PromotionGateReader reads every gate recorded on a promotion, the
// pre-promote ones and those appended after it: what says when an attempt of
// it shipped (`forge obs check`, releasehealth.LiveAt). Only the cloud backend
// implements it: a local env has no promotions.
//
//forge:contract
type PromotionGateReader interface {
	PromotionGates(ctx context.Context, promotionID string) ([]release.Gate, error)
}

// WorkloadReader reads the env's per-workload restarts, crash loops, last
// termination reasons and failing jobs (`forge obs workloads`). Only the cloud
// backend implements it: a local stack's workloads are host processes or a
// cluster the user already has kubectl for.
//
//forge:contract
type WorkloadReader interface {
	Workloads(ctx context.Context, n Narrow) (WorkloadsResult, error)
}

// TraceExtentReader reads when one trace ran, from its spans: the range
// `forge obs logs --trace` reads when none was named. Both backends implement
// it.
//
//forge:contract
type TraceExtentReader interface {
	TraceExtent(ctx context.Context, req TraceRequest) (TraceExtent, error)
}

// DashboardReader replays the console's dashboards: the index it lists for an
// env, a dashboard's spec, and one panel run the way the console runs it, read
// back as counts (`forge obs dashboards audit`). Only the cloud backend
// implements it: a local ClickStack stores no dashboards.
//
//forge:contract
type DashboardReader interface {
	ListDashboards(ctx context.Context, project string) ([]DashboardInfo, error)
	GetDashboard(ctx context.Context, ref DashboardRef) (DashboardDocument, error)
	QueryPanelReadout(ctx context.Context, req PanelReadoutRequest) (PanelReadout, error)
}

// PGOExporter reads an env's merged CPU profile in the shape `go build -pgo`
// takes: what `forge pgo pull` fetches from the control plane. Only the cloud
// backend implements it; a ClickStack's profiles are read through
// pkg/profilequery instead.
//
//forge:contract
type PGOExporter interface {
	ExportPGOProfile(ctx context.Context, req ExportPGORequest) (ExportPGOResult, error)
}
