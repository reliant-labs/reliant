package obsbackend

import "context"

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

// AlertReader lists the env's active alerts, firing or pending. Only the
// cloud backend implements it: locally the alert state is the `alerts`
// workload's own (`forge obs alerts check` evaluates the rules once).
//
//forge:contract
type AlertReader interface {
	Alerts(ctx context.Context) ([]Alert, error)
}

// DeliveryReader reads whether the control plane has delivered a working
// telemetry token into the clusters that ship the env's telemetry. Only the
// cloud backend implements it: a local stack needs no token.
//
//forge:contract
type DeliveryReader interface {
	TelemetryDeliveries(ctx context.Context) (DeliveriesResult, error)
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
