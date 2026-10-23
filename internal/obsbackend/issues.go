package obsbackend

// Issues: errors grouped by fingerprint, read the same way on both backends.
// The local backend groups ERROR logs at query time with sfp1 and keeps no
// state; the cloud backend reads the control plane's IssuesService, which does
// (status, first/last seen across retention, regression by release).
//
// The vocabulary here is the verbs': plain strings for statuses and platforms,
// times as time.Time. The wire shapes (protojson, camelCase, enum names) live
// in cloud_issues.go.

import "time"

// Issue statuses (DESIGN §5.5). A local issue is always IssueUnresolved.
const (
	IssueNew        = "new"
	IssueUnresolved = "unresolved"
	IssueResolved   = "resolved"
	IssueRegressed  = "regressed"
	IssueIgnored    = "ignored"
)

// IssueStatuses are the statuses a request may filter on, in lifecycle order.
var IssueStatuses = []string{IssueNew, IssueUnresolved, IssueResolved, IssueRegressed, IssueIgnored}

// Issue platforms.
const (
	PlatformServer  = "server"
	PlatformBrowser = "browser"
)

// Issue list sorts are obsquery.IssueSortLast, IssueSortFirst and IssueSortEvents.

// Resolve-in kinds of a resolve.
const (
	ResolveNow         = "now"
	ResolveNextRelease = "next"
	ResolveVersion     = "version"
)

// Issue is one group of errors.
type Issue struct {
	// ID is what `forge obs issue <id>` takes: "sfp1-<fingerprint>" locally,
	// the control plane's id in the cloud.
	ID       string
	Platform string
	// EnvID is the control plane's id for the issue's environment (cloud only).
	EnvID         string
	Service       string
	Fingerprint   string
	Title         string
	Culprit       string
	ExceptionType string
	Status        string
	FirstSeen     time.Time
	LastSeen      time.Time
	FirstVersion  string
	LastVersion   string
	// Events and Affected are over the queried range; Affected counts traces
	// (server) or sessions (browser).
	Events   int64
	Affected int64

	ResolvedAt      time.Time
	ResolvedVersion string
	RegressedAt     time.Time
	IgnoredUntil    time.Time
	// Version is the row version a triage write must echo; 0 locally.
	Version int32
	// LatestTraceID is the trace of the newest event, when the list knows it
	// (the local grouping does; the control plane answers it in GetIssue).
	LatestTraceID string
}

// IssueEvent is one occurrence of an issue.
type IssueEvent struct {
	Time       time.Time
	TraceID    string
	SpanID     string
	Version    string
	Message    string
	Attributes map[string]string
	SessionID  string
	URLPath    string
}

// IssueFrame is one stack frame, innermost first.
type IssueFrame struct {
	Function string
	File     string
	Line     int
	InApp    bool
}

// IssueVersion is the events of an issue in one service version.
type IssueVersion struct {
	Version   string
	Events    int64
	Affected  int64
	FirstSeen time.Time
	LastSeen  time.Time
}

// IssueRelease is the release an issue first appeared in.
type IssueRelease struct {
	Version     string
	GitCommit   string
	CommitRange string
	PromotedAt  time.Time
}

// ListIssuesRequest lists the issues of the narrowed scope.
type ListIssuesRequest struct {
	Narrow
	// Statuses filters on these (IssueStatuses); empty is the open ones.
	Statuses []string
	// Platform is "", PlatformServer or PlatformBrowser.
	Platform string
	// SinceRelease keeps issues first seen in this release version.
	SinceRelease string
	// SincePromotion keeps issues first seen at or after this promotion.
	SincePromotion string
	Sort           string
	Limit          int
	PageToken      string
}

// ListIssuesResult is a page of issues.
type ListIssuesResult struct {
	Issues []Issue
	// NextPageToken resumes the cloud list. A local list has no pages:
	// Truncated says the Limit cut it.
	NextPageToken string
	Truncated     bool
	Statements    []Statement
}

// GetIssueRequest reads one issue and what is needed to fix it.
type GetIssueRequest struct {
	Narrow
	ID string
}

// GetIssueResult is one issue in full.
type GetIssueResult struct {
	Issue       Issue
	LatestEvent *IssueEvent
	Frames      []IssueFrame
	ByVersion   []IssueVersion
	// CoOccurring are the ids of issues that fired in the same traces.
	CoOccurring  []string
	FirstRelease *IssueRelease
	Statements   []Statement
}

// ListIssueEventsRequest lists the newest events of an issue.
type ListIssueEventsRequest struct {
	Narrow
	ID    string
	Limit int
}

// ListIssueEventsResult is a page of events, newest first.
type ListIssueEventsResult struct {
	Events        []IssueEvent
	NextPageToken string
	Truncated     bool
	Statements    []Statement
}

// UpdateIssueStatusRequest triages an issue.
type UpdateIssueStatusRequest struct {
	ID string
	// Status is IssueResolved, IssueIgnored or IssueUnresolved.
	Status string
	// ResolveIn is ResolveNow (default), ResolveNextRelease or ResolveVersion;
	// Version names it for ResolveVersion.
	ResolveIn string
	Version   string
	// IgnoredUntil un-ignores at this time; zero ignores until unignored.
	IgnoredUntil time.Time
}

// UpdateIssueStatusResult is the issue after the write.
type UpdateIssueStatusResult struct {
	Issue Issue
}
