package obsbackend

// The cloud sessions reader: the control plane's RumService, by procedure name,
// with locally declared protojson shapes (camelCase fields, enum names as
// strings, int64 as strings). Field names are those of the control plane's
// proto/services/rum/v1/rum.proto. GetSessionReplay is not called: replay bytes
// are for the console's player, not for an agent's context.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
)

const (
	procListSessions       = "controlplane.v1.RumService/ListSessions"
	procGetSession         = "controlplane.v1.RumService/GetSession"
	procGetSessionTimeline = "controlplane.v1.RumService/GetSessionTimeline"
)

// wireRumScope is RumScope: the org, the env and the services. The time range
// is optional for a read of one named session, where unset means "find it
// within 30 days".
type wireRumScope struct {
	OrgID     string         `json:"orgId"`
	EnvIDs    []string       `json:"envIds,omitempty"`
	Services  []string       `json:"services,omitempty"`
	TimeRange *wireTimeRange `json:"timeRange,omitempty"`
}

func (c Cloud) rumScope(n Narrow, withRange bool) (wireRumScope, error) {
	if len(n.Filters) > 0 {
		return wireRumScope{}, Wrong("--filter narrows telemetry attributes and does not apply to sessions; use --service")
	}
	s := c.scope(n)
	out := wireRumScope{OrgID: s.OrgID, EnvIDs: s.EnvIDs, Services: s.Services}
	if withRange {
		out.TimeRange = &s.TimeRange
	}
	return out, nil
}

type wireSession struct {
	SessionID      string    `json:"sessionId"`
	Service        string    `json:"service"`
	ServiceVersion string    `json:"serviceVersion"`
	UserID         string    `json:"userId"`
	EntryPath      string    `json:"entryPath"`
	StartedAt      time.Time `json:"startedAt"`
	EndedAt        time.Time `json:"endedAt"`
	PageViews      jsonInt   `json:"pageViews"`
	Interactions   jsonInt   `json:"interactions"`
	Errors         jsonInt   `json:"errors"`
	HasReplay      bool      `json:"hasReplay"`
}

func (w wireSession) summary() SessionSummary {
	return SessionSummary{
		ID: w.SessionID, Service: w.Service, Version: w.ServiceVersion, UserID: w.UserID, EntryPath: w.EntryPath,
		Started: w.StartedAt.UTC(), Ended: w.EndedAt.UTC(),
		PageViews: int64(w.PageViews), Interactions: int64(w.Interactions), Errors: int64(w.Errors), HasReplay: w.HasReplay,
	}
}

type wireTimelineEntry struct {
	Time        time.Time         `json:"time"`
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	Path        string            `json:"path"`
	Method      string            `json:"method"`
	Status      int               `json:"status"`
	Selector    string            `json:"selector"`
	Element     string            `json:"element"`
	ErrorType   string            `json:"errorType"`
	Message     string            `json:"message"`
	Fingerprint string            `json:"fingerprint"`
	IssueID     string            `json:"issueId"`
	TraceID     string            `json:"traceId"`
	SpanID      string            `json:"spanId"`
	DurationMs  float64           `json:"durationMs"`
	Props       map[string]string `json:"props"`
	Service     string            `json:"service"`
}

func (w wireTimelineEntry) entry() SessionEntry {
	return SessionEntry{
		Time: w.Time.UTC(), Kind: strings.ToLower(strings.TrimPrefix(w.Kind, "SESSION_TIMELINE_KIND_")), Name: w.Name,
		Path: w.Path, Method: w.Method, Status: w.Status, Selector: w.Selector, Element: w.Element, ErrorType: w.ErrorType,
		Message: w.Message, Fingerprint: w.Fingerprint, IssueID: w.IssueID, TraceID: w.TraceID, SpanID: w.SpanID,
		DurationMs: w.DurationMs, Props: w.Props, Service: w.Service,
	}
}

// ListSessions implements SessionReader over RumService.ListSessions.
func (c Cloud) ListSessions(ctx context.Context, req ListSessionsRequest) (ListSessionsResult, error) {
	if req.Explain {
		return ListSessionsResult{}, Undetermined("--explain compiles the local backend's SQL; hosted sessions are compiled by the control plane")
	}
	scope, err := c.rumScope(req.Narrow, true)
	if err != nil {
		return ListSessionsResult{}, err
	}
	in := struct {
		Scope        wireRumScope `json:"scope"`
		HasError     bool         `json:"hasError,omitempty"`
		HasReplay    bool         `json:"hasReplay,omitempty"`
		UserID       string       `json:"userId,omitempty"`
		PathContains string       `json:"pathContains,omitempty"`
		PageSize     int          `json:"pageSize,omitempty"`
		PageToken    string       `json:"pageToken,omitempty"`
	}{scope, req.HasError, req.HasReplay, req.UserID, req.PathContains, req.Limit, req.PageToken}
	var out struct {
		Sessions      []wireSession `json:"sessions"`
		NextPageToken string        `json:"nextPageToken"`
	}
	if err := c.call(ctx, procListSessions, in, &out); err != nil {
		return ListSessionsResult{}, err
	}
	res := ListSessionsResult{NextPageToken: out.NextPageToken}
	for _, w := range out.Sessions {
		res.Sessions = append(res.Sessions, w.summary())
	}
	return res, nil
}

// GetSession implements SessionReader: GetSession, then one page of
// GetSessionTimeline. A session the scope cannot see is NotFound from both, so
// the summary answers it and the timeline is not asked for.
func (c Cloud) GetSession(ctx context.Context, req GetSessionRequest) (GetSessionResult, error) {
	if req.Explain {
		return GetSessionResult{}, Undetermined("--explain compiles the local backend's SQL; hosted sessions are compiled by the control plane")
	}
	scope, err := c.rumScope(req.Narrow, !req.Range.From.IsZero() && !req.Range.To.IsZero())
	if err != nil {
		return GetSessionResult{}, err
	}
	var sum struct {
		Session wireSession `json:"session"`
	}
	if err := c.callSession(ctx, procGetSession, req.ID, struct {
		Scope     wireRumScope `json:"scope"`
		SessionID string       `json:"sessionId"`
	}{scope, req.ID}, &sum); err != nil {
		return GetSessionResult{}, err
	}
	var tl struct {
		Entries       []wireTimelineEntry `json:"entries"`
		NextPageToken string              `json:"nextPageToken"`
	}
	if err := c.callSession(ctx, procGetSessionTimeline, req.ID, struct {
		Scope     wireRumScope `json:"scope"`
		SessionID string       `json:"sessionId"`
		PageSize  int          `json:"pageSize,omitempty"`
		PageToken string       `json:"pageToken,omitempty"`
	}{scope, req.ID, req.Limit, req.PageToken}, &tl); err != nil {
		return GetSessionResult{}, err
	}
	res := GetSessionResult{Session: sum.Session.summary(), NextPageToken: tl.NextPageToken}
	for _, e := range tl.Entries {
		res.Timeline = append(res.Timeline, e.entry())
	}
	return res, nil
}

// callSession is call with the one answer a keyed read turns into a sentence:
// NotFound is "that session is not there", the request's fault (exit 1).
func (c Cloud) callSession(ctx context.Context, procedure, id string, req, out any) error {
	err := c.call(ctx, procedure, req, out)
	var ce *cloud.Error
	if errors.As(err, &ce) && ce.HasCode(cloud.CodeNotFound) {
		return errSessionNotFound(id).Cause(err)
	}
	return err
}
