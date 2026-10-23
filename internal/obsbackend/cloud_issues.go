package obsbackend

// The cloud issues reader: the control plane's IssuesService, by procedure
// name, with locally declared protojson shapes (camelCase fields, enum names as
// strings, int64 as strings). Field names are those of the control plane's
// proto/services/issues/v1/issues.proto and proto/controlplane/v1/issues.proto.

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
)

const (
	procListIssues        = "controlplane.v1.IssuesService/ListIssues"
	procGetIssue          = "controlplane.v1.IssuesService/GetIssue"
	procListIssueEvents   = "controlplane.v1.IssuesService/ListIssueEvents"
	procUpdateIssueStatus = "controlplane.v1.IssuesService/UpdateIssueStatus"
)

// wireIssueScope is IssueScope: a QueryScope without the telemetry-only
// filters (the control plane reserves that field and refuses it).
type wireIssueScope struct {
	OrgID     string        `json:"orgId"`
	EnvIDs    []string      `json:"envIds,omitempty"`
	Services  []string      `json:"services,omitempty"`
	TimeRange wireTimeRange `json:"timeRange"`
}

func (c Cloud) issueScope(n Narrow) (wireIssueScope, error) {
	if len(n.Filters) > 0 {
		return wireIssueScope{}, Wrong("--filter narrows telemetry attributes and does not apply to issues; use --service")
	}
	s := c.scope(n)
	return wireIssueScope{OrgID: s.OrgID, EnvIDs: s.EnvIDs, Services: s.Services, TimeRange: s.TimeRange}, nil
}

// callIssues is call with the one answer an issues read must not blur: a
// control plane that does not serve issues yet is "could not look", and says so.
func (c Cloud) callIssues(ctx context.Context, procedure string, req, out any) error {
	err := c.call(ctx, procedure, req, out)
	var ce *cloud.Error
	if errors.As(err, &ce) && ce.HasCode(cloud.CodeUnimplemented) {
		return Undetermined("the control plane does not serve issues yet (%s is unimplemented there)", procedure).Cause(err)
	}
	return err
}

// ── wire shapes ─────────────────────────────────────────────────────────

type wireTrendPoint struct {
	Hour   time.Time `json:"hour"`
	Events jsonInt   `json:"events"`
}

type wireIssue struct {
	ID              string           `json:"id"`
	Platform        string           `json:"platform"`
	EnvID           string           `json:"envId"`
	Service         string           `json:"service"`
	Fingerprint     string           `json:"fingerprint"`
	Title           string           `json:"title"`
	Culprit         string           `json:"culprit"`
	ExceptionType   string           `json:"exceptionType"`
	Status          string           `json:"status"`
	FirstSeenAt     time.Time        `json:"firstSeenAt"`
	FirstVersion    string           `json:"firstVersion"`
	LastSeenAt      time.Time        `json:"lastSeenAt"`
	LastVersion     string           `json:"lastVersion"`
	Events          jsonInt          `json:"events"`
	Affected        jsonInt          `json:"affected"`
	Trend           []wireTrendPoint `json:"trend"`
	ResolvedAt      time.Time        `json:"resolvedAt"`
	ResolvedVersion string           `json:"resolvedVersion"`
	RegressedAt     time.Time        `json:"regressedAt"`
	IgnoredUntil    time.Time        `json:"ignoredUntil"`
	Version         int32            `json:"version"`
}

// wireStatus and wirePlatform name the control plane's enum values.
var (
	wireStatus = map[string]string{
		"ISSUE_STATUS_NEW": IssueNew, "ISSUE_STATUS_UNRESOLVED": IssueUnresolved, "ISSUE_STATUS_RESOLVED": IssueResolved,
		"ISSUE_STATUS_REGRESSED": IssueRegressed, "ISSUE_STATUS_IGNORED": IssueIgnored,
	}
	wirePlatform = map[string]string{"ISSUE_PLATFORM_SERVER": PlatformServer, "ISSUE_PLATFORM_BROWSER": PlatformBrowser}
)

func statusEnum(s string) string { return "ISSUE_STATUS_" + upper(s) }

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

func (w wireIssue) issue() Issue {
	return Issue{
		ID: w.ID, Platform: wirePlatform[w.Platform], Service: w.Service, Fingerprint: w.Fingerprint,
		Title: w.Title, Culprit: w.Culprit, ExceptionType: w.ExceptionType, Status: wireStatus[w.Status],
		FirstSeen: w.FirstSeenAt.UTC(), LastSeen: w.LastSeenAt.UTC(), FirstVersion: w.FirstVersion, LastVersion: w.LastVersion,
		Events: int64(w.Events), Affected: int64(w.Affected),
		ResolvedAt: w.ResolvedAt.UTC(), ResolvedVersion: w.ResolvedVersion, RegressedAt: w.RegressedAt.UTC(),
		IgnoredUntil: w.IgnoredUntil.UTC(), Version: w.Version,
	}
}

type wireIssueEvent struct {
	Timestamp      time.Time         `json:"timestamp"`
	TraceID        string            `json:"traceId"`
	SpanID         string            `json:"spanId"`
	ServiceVersion string            `json:"serviceVersion"`
	Message        string            `json:"message"`
	Attributes     map[string]string `json:"attributes"`
	SessionID      string            `json:"sessionId"`
	URLPath        string            `json:"urlPath"`
}

func (w wireIssueEvent) event() IssueEvent {
	return IssueEvent{
		Time: w.Timestamp.UTC(), TraceID: w.TraceID, SpanID: w.SpanID, Version: w.ServiceVersion, Message: w.Message,
		Attributes: w.Attributes, SessionID: w.SessionID, URLPath: w.URLPath,
	}
}

// ── ListIssues ──────────────────────────────────────────────────────────

var sortEnum = map[string]string{
	"": "ISSUE_SORT_LAST_SEEN_DESC", "last": "ISSUE_SORT_LAST_SEEN_DESC",
	"first": "ISSUE_SORT_FIRST_SEEN_DESC", "events": "ISSUE_SORT_EVENTS_DESC",
}

// ListIssues implements IssueReader over IssuesService.ListIssues.
func (c Cloud) ListIssues(ctx context.Context, req ListIssuesRequest) (ListIssuesResult, error) {
	if req.Explain {
		return ListIssuesResult{}, Undetermined("--explain compiles the local backend's SQL; hosted issues are grouped by the control plane's ingest view and have no statement to show")
	}
	scope, err := c.issueScope(req.Narrow)
	if err != nil {
		return ListIssuesResult{}, err
	}
	sort, ok := sortEnum[req.Sort]
	if !ok {
		return ListIssuesResult{}, Wrong("unknown --sort %q (use last, first or events)", req.Sort)
	}
	in := struct {
		Scope            wireIssueScope `json:"scope"`
		Statuses         []string       `json:"statuses,omitempty"`
		Platform         string         `json:"platform,omitempty"`
		FirstSeenRelease string         `json:"firstSeenRelease,omitempty"`
		SincePromotionID string         `json:"sincePromotionId,omitempty"`
		Sort             string         `json:"sort"`
		PageSize         int            `json:"pageSize,omitempty"`
		PageToken        string         `json:"pageToken,omitempty"`
	}{Scope: scope, FirstSeenRelease: req.SinceRelease, SincePromotionID: req.SincePromotion, Sort: sort, PageSize: req.Limit, PageToken: req.PageToken}
	for _, s := range req.Statuses {
		in.Statuses = append(in.Statuses, statusEnum(s))
	}
	if req.Platform != "" {
		in.Platform = "ISSUE_PLATFORM_" + upper(req.Platform)
	}
	var out struct {
		Issues        []wireIssue `json:"issues"`
		NextPageToken string      `json:"nextPageToken"`
	}
	if err := c.callIssues(ctx, procListIssues, in, &out); err != nil {
		return ListIssuesResult{}, err
	}
	res := ListIssuesResult{NextPageToken: out.NextPageToken}
	for _, w := range out.Issues {
		res.Issues = append(res.Issues, w.issue())
	}
	return res, nil
}

// ── GetIssue ────────────────────────────────────────────────────────────

// getIssue is the shared read: GetIssue plus the raw wire issue.
func (c Cloud) getIssue(ctx context.Context, id string, n Narrow) (GetIssueResult, error) {
	in := struct {
		IssueID   string        `json:"issueId"`
		TimeRange wireTimeRange `json:"timeRange"`
	}{id, wireTimeRange{Start: n.Range.From.UTC(), End: n.Range.To.UTC()}}
	var out struct {
		Issue       wireIssue       `json:"issue"`
		LatestEvent *wireIssueEvent `json:"latestEvent"`
		Frames      []struct {
			Function string `json:"function"`
			File     string `json:"file"`
			Line     int    `json:"line"`
			InApp    bool   `json:"inApp"`
		} `json:"frames"`
		ByVersion []struct {
			Version     string    `json:"version"`
			Events      jsonInt   `json:"events"`
			Affected    jsonInt   `json:"affected"`
			FirstSeenAt time.Time `json:"firstSeenAt"`
			LastSeenAt  time.Time `json:"lastSeenAt"`
		} `json:"byVersion"`
		CoOccurring  []string `json:"coOccurringIssueIds"`
		FirstRelease *struct {
			Version     string    `json:"version"`
			GitCommit   string    `json:"gitCommit"`
			CommitRange string    `json:"commitRange"`
			PromotedAt  time.Time `json:"promotedAt"`
		} `json:"firstRelease"`
	}
	if err := c.callIssues(ctx, procGetIssue, in, &out); err != nil {
		return GetIssueResult{}, err
	}
	res := GetIssueResult{Issue: out.Issue.issue(), CoOccurring: out.CoOccurring}
	res.Issue.EnvID = out.Issue.EnvID
	if out.LatestEvent != nil {
		ev := out.LatestEvent.event()
		res.LatestEvent = &ev
		res.Issue.LatestTraceID = ev.TraceID
	}
	for _, f := range out.Frames {
		res.Frames = append(res.Frames, IssueFrame{Function: f.Function, File: f.File, Line: f.Line, InApp: f.InApp})
	}
	for _, v := range out.ByVersion {
		res.ByVersion = append(res.ByVersion, IssueVersion{
			Version: v.Version, Events: int64(v.Events), Affected: int64(v.Affected),
			FirstSeen: v.FirstSeenAt.UTC(), LastSeen: v.LastSeenAt.UTC(),
		})
	}
	if r := out.FirstRelease; r != nil && r.Version != "" {
		res.FirstRelease = &IssueRelease{Version: r.Version, GitCommit: r.GitCommit, CommitRange: r.CommitRange, PromotedAt: r.PromotedAt.UTC()}
	}
	return res, nil
}

// GetIssue implements IssueReader over IssuesService.GetIssue. An issue of
// another environment than the one named is not served: the id is the
// control plane's, but the document says which env it is about.
func (c Cloud) GetIssue(ctx context.Context, req GetIssueRequest) (GetIssueResult, error) {
	if req.Explain {
		return GetIssueResult{}, Undetermined("--explain compiles the local backend's SQL; hosted issues have no statement to show")
	}
	res, err := c.getIssue(ctx, req.ID, req.Narrow)
	if err != nil {
		return res, err
	}
	return res, c.checkEnv(res.Issue)
}

func (c Cloud) checkEnv(i Issue) error {
	if c.EnvID != "" && i.EnvID != "" && i.EnvID != c.EnvID {
		return Wrong("issue %s belongs to another environment than %s", i.ID, c.Env)
	}
	return nil
}

// ── ListIssueEvents ─────────────────────────────────────────────────────

// ListIssueEvents implements IssueReader. The time range is left to the
// control plane's default (the issue's first to its last event): an issue's
// newest events are wanted whatever the verb's --since says.
func (c Cloud) ListIssueEvents(ctx context.Context, req ListIssueEventsRequest) (ListIssueEventsResult, error) {
	if req.Explain {
		return ListIssueEventsResult{}, Undetermined("--explain compiles the local backend's SQL; hosted issues have no statement to show")
	}
	in := struct {
		IssueID  string `json:"issueId"`
		PageSize int    `json:"pageSize,omitempty"`
	}{req.ID, req.Limit}
	var out struct {
		Events        []wireIssueEvent `json:"events"`
		NextPageToken string           `json:"nextPageToken"`
	}
	if err := c.callIssues(ctx, procListIssueEvents, in, &out); err != nil {
		return ListIssueEventsResult{}, err
	}
	res := ListIssueEventsResult{NextPageToken: out.NextPageToken, Truncated: out.NextPageToken != ""}
	for _, w := range out.Events {
		res.Events = append(res.Events, w.event())
	}
	return res, nil
}

// ── UpdateIssueStatus ───────────────────────────────────────────────────

// triageWindow is the window of the read that precedes a write; only the
// issue's row version and environment are used from it.
const triageWindow = time.Hour

// UpdateIssueStatus implements IssueReader. It reads the issue first, to learn
// its environment and row version, and sends the version back as
// expected_version: a write over a state the caller never saw is refused
// (Aborted), and the caller is told to re-read.
func (c Cloud) UpdateIssueStatus(ctx context.Context, req UpdateIssueStatusRequest) (UpdateIssueStatusResult, error) {
	switch req.Status {
	case IssueResolved, IssueIgnored, IssueUnresolved:
	default:
		return UpdateIssueStatusResult{}, Wrong("cannot set an issue to %q; use %s, %s or %s", req.Status, IssueResolved, IssueIgnored, IssueUnresolved)
	}
	type resolveIn struct {
		Kind    string `json:"kind"`
		Version string `json:"version,omitempty"`
	}
	var rin *resolveIn
	if req.Status == IssueResolved {
		switch req.ResolveIn {
		case "", ResolveNow:
			rin = &resolveIn{Kind: "ISSUE_RESOLVE_KIND_NOW"}
		case ResolveNextRelease:
			rin = &resolveIn{Kind: "ISSUE_RESOLVE_KIND_NEXT_RELEASE"}
		case ResolveVersion:
			if req.Version == "" {
				return UpdateIssueStatusResult{}, Wrong("resolving in a version needs the version")
			}
			rin = &resolveIn{Kind: "ISSUE_RESOLVE_KIND_VERSION", Version: req.Version}
		default:
			return UpdateIssueStatusResult{}, Wrong("unknown resolve-in %q", req.ResolveIn)
		}
	}
	now := time.Now().UTC()
	cur, err := c.getIssue(ctx, req.ID, Narrow{Range: Range{From: now.Add(-triageWindow), To: now}})
	if err != nil {
		return UpdateIssueStatusResult{}, err
	}
	if err := c.checkEnv(cur.Issue); err != nil {
		return UpdateIssueStatusResult{}, err
	}
	envID := cur.Issue.EnvID
	if envID == "" {
		envID = c.EnvID
	}
	in := struct {
		IssueID         string     `json:"issueId"`
		EnvID           string     `json:"envId"`
		Status          string     `json:"status"`
		ResolveIn       *resolveIn `json:"resolveIn,omitempty"`
		IgnoredUntil    *time.Time `json:"ignoredUntil,omitempty"`
		ExpectedVersion int32      `json:"expectedVersion,omitempty"`
	}{IssueID: req.ID, EnvID: envID, ResolveIn: rin, ExpectedVersion: cur.Issue.Version,
		Status: "ISSUE_STATUS_CHANGE_" + upper(req.Status)}
	if req.Status == IssueIgnored && !req.IgnoredUntil.IsZero() {
		t := req.IgnoredUntil.UTC()
		in.IgnoredUntil = &t
	}
	var out struct {
		Issue wireIssue `json:"issue"`
	}
	if err := c.callIssues(ctx, procUpdateIssueStatus, in, &out); err != nil {
		var ce *cloud.Error
		if errors.As(err, &ce) && ce.HasCode("aborted") {
			return UpdateIssueStatusResult{}, Wrong("issue %s changed since it was read; re-read and retry", req.ID).Cause(err)
		}
		return UpdateIssueStatusResult{}, err
	}
	i := out.Issue.issue()
	i.EnvID = out.Issue.EnvID
	return UpdateIssueStatusResult{Issue: i}, nil
}
