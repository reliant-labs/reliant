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
	// Source is the issue source (obsquery.LogKeyIssueSource) its events
	// carry; "" for an error.
	Source string
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
	// Events and Affected are over the queried range. Affected counts
	// AffectedUnit: users when the issue's events name them, else the chats,
	// threads or workflows they ran for, else traces (server) or sessions
	// (browser). Never a bare number: an unknown unit is "".
	Events       int64
	Affected     int64
	AffectedUnit string

	ResolvedAt      time.Time
	ResolvedVersion string
	RegressedAt     time.Time
	IgnoredUntil    time.Time
	// Version is the row version a triage write must echo; 0 locally.
	Version int32
	// LatestTraceID is the trace of the newest event, when the list knows it
	// (the local grouping does; the control plane answers it in GetIssue).
	LatestTraceID string
	// Assignee is who the issue is assigned to; nil when unassigned (always
	// nil locally).
	Assignee *IssueUser
	// IncidentKey is shared by the issues of one incident: issues with the
	// same cause (obsquery.ServerCause) that first fired within a window of
	// one another. Empty for an issue that is not part of one. RelatedCount
	// is how many OTHER issues carry the same key. Issues are never merged.
	IncidentKey  string
	RelatedCount int
}

// IssueUser is a person as an issue shows them: an assignee, an author.
type IssueUser struct {
	ID   string
	Name string
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
	Version      string
	Events       int64
	Affected     int64
	AffectedUnit string
	FirstSeen    time.Time
	LastSeen     time.Time
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
	// Source lists the issues of this issue source; "" lists errors.
	Source string
	// Assignee keeps issues assigned to this user id, or "me" (the verified
	// credential's acting user). Empty keeps every issue unless Unassigned.
	Assignee string
	// Unassigned keeps issues with no assignee; exclusive with Assignee.
	Unassigned bool
	// SinceRelease keeps issues first seen in this release version.
	SinceRelease string
	// SincePromotion keeps issues first seen at or after this promotion.
	SincePromotion string
	Sort           string
	Limit          int
	PageToken      string
	// IncidentWindow is how close in first-seen time issues of one cause must
	// be to share an incident; zero is obsquery.DefaultIncidentWindow.
	IncidentWindow time.Duration
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
	CoOccurring []string
	// Incident are the ids of the other issues of this issue's incident (the
	// same cause, first fired close in time), when it is part of one.
	Incident     []string
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

// Timeline entry kinds, and the kinds of actor that made one.
const (
	TimelineStatus     = "status"
	TimelineComment    = "comment"
	TimelineAssignment = "assignment"
	TimelineChat       = "chat"

	ActorUser    = "user"
	ActorMachine = "machine"
	ActorSystem  = "system"
)

// IssueActor is who did something: a person, a machine credential or the
// system.
type IssueActor struct {
	Kind string
	ID   string
	Name string
}

// StatusChange is a status entry. From is "" for the issue's opening.
type StatusChange struct {
	From, To     string
	Version      string
	IgnoredUntil time.Time
}

// IssueComment is one comment on an issue. Body is plain text or markdown,
// verbatim.
type IssueComment struct {
	ID        string
	IssueID   string
	Author    *IssueUser
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AssignmentChange is an assignment entry; a nil side is "unassigned".
type AssignmentChange struct {
	From, To *IssueUser
}

// ChatLink is a chat started from the issue.
type ChatLink struct {
	ChatID    string
	ChatTitle string
	LinkedBy  *IssueUser
	LinkedAt  time.Time
}

// TimelineEntry is one thing that happened to an issue. Kind says which of
// Status, Comment, Assignment and Chat is set.
type TimelineEntry struct {
	ID    string
	Time  time.Time
	Kind  string
	Actor IssueActor
	// IssueID is the issue it happened on: the requested one, or one merged
	// into it.
	IssueID    string
	Status     *StatusChange
	Comment    *IssueComment
	Assignment *AssignmentChange
	Chat       *ChatLink
}

// ListIssueTimelineRequest reads the newest Limit entries of an issue's
// timeline.
type ListIssueTimelineRequest struct {
	ID    string
	Limit int
}

// ListIssueTimelineResult is the newest entries, oldest first.
type ListIssueTimelineResult struct {
	Entries []TimelineEntry
	// IssueID is the issue the timeline is of: the requested one, or the one
	// it was merged into.
	IssueID string
	// Omitted counts older entries the Limit cut.
	Omitted int
}

// AddIssueCommentRequest comments on an issue.
type AddIssueCommentRequest struct {
	ID   string
	Body string
}

// AddIssueCommentResult is the comment as stored.
type AddIssueCommentResult struct {
	Comment IssueComment
}

// AssignIssueRequest assigns an issue. Assignee is a user id or "me" (resolved
// by the server from the credential's acting user); "" clears it.
type AssignIssueRequest struct {
	ID       string
	Assignee string
}

// AssignIssueResult is the issue after the write.
type AssignIssueResult struct {
	Issue Issue
}
