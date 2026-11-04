package obsbackend

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// Local sessions: pkg/obsquery's session compilers over the local ClickStack.
// The session tables (rum_sessions, error_events, error_stacks) are the control
// plane's derived views; a ClickStack that does not have them answers "could
// not look", with the remedy, rather than "the request is wrong".

const (
	// sessionLookback is how far back a local read of one session looks when
	// the caller gave no range.
	sessionLookback = obsquery.MaxRangeSessions
	// timelineLead and timelineTail pad the session's own span: a span is
	// exported after the moment it describes.
	timelineLead = time.Minute
	timelineTail = 5 * time.Minute
)

// localSessionScope is the scope of a local session read. The env is not a
// filter: browser rows carry the control plane's env id, which a local stack
// does not have.
func localSessionScope(n Narrow) obsquery.Scope {
	return obsquery.Scope{Database: obsquery.LocalDatabase, Local: true, Services: n.Services, Start: n.Range.From, End: n.Range.To}
}

// errSessionNotFound is exit 1: the session id names nothing the scope can see.
func errSessionNotFound(id string) *Error {
	return Wrong("session %s not found in the environment (or it is older than the 30 days sessions are kept)", id)
}

// sessionErr maps a session read's failure onto the exit table.
func sessionErr(err error) error {
	var be *Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &be):
		if strings.Contains(be.Msg, "UNKNOWN_TABLE") || strings.Contains(be.Msg, "Unknown table") {
			return Undetermined("this ClickStack has no browser session tables (rum_sessions, error_events): sessions are derived by the control plane; read them on a hosted env").Cause(err)
		}
		return err
	case errors.Is(err, obsquery.ErrInvalid):
		return compileErr(err)
	}
	return Undetermined("the local ClickStack returned sessions forge cannot read").Cause(err)
}

// rows runs st and hands its result to a obsquery Parse function.
func (l Local) rows(ctx context.Context, st obsquery.Stmt) (obsquery.Rows, error) {
	r, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return nil, sessionErr(err)
	}
	return RowsOf(r), nil
}

func sessionSummary(s obsquery.SessionSummary) SessionSummary {
	return SessionSummary{
		ID: s.SessionID, Service: s.Service, Version: s.Version, UserID: s.UserID, EntryPath: s.EntryPath,
		Started: s.Started.UTC(), Ended: s.Ended.UTC(),
		PageViews: int64(s.PageViews), Interactions: int64(s.Interactions), Errors: int64(s.Errors), HasReplay: s.HasReplay,
	}
}

// ListSessions implements SessionReader.
func (l Local) ListSessions(ctx context.Context, req ListSessionsRequest) (ListSessionsResult, error) {
	cursor, err := obsquery.DecodeSessionCursor(req.PageToken)
	if err != nil {
		return ListSessionsResult{}, compileErr(err)
	}
	q := obsquery.SessionQuery{
		Scope: localSessionScope(req.Narrow), HasError: req.HasError, HasReplay: req.HasReplay,
		UserID: req.UserID, PathContains: req.PathContains, Limit: req.Limit, Cursor: cursor,
	}
	st, err := q.Compile()
	if err != nil {
		return ListSessionsResult{}, compileErr(err)
	}
	res := ListSessionsResult{Statements: []Statement{stmt("sessions", st)}}
	if req.Explain {
		return res, nil
	}
	rows, err := l.rows(ctx, st)
	if err != nil {
		return res, err
	}
	page, err := q.Parse(rows)
	if err != nil {
		return res, sessionErr(err)
	}
	res.NextPageToken = page.NextCursor
	for _, s := range page.Sessions {
		res.Sessions = append(res.Sessions, sessionSummary(s))
	}
	return res, nil
}

// GetSession implements SessionReader: the summary first, which also tells the
// timeline read which days to look at.
func (l Local) GetSession(ctx context.Context, req GetSessionRequest) (GetSessionResult, error) {
	rng := req.Range
	if rng.From.IsZero() || rng.To.IsZero() {
		to := req.Now
		if to.IsZero() {
			to = time.Now()
		}
		rng = Range{From: to.Add(-sessionLookback).UTC(), To: to.UTC()}
	}
	n := req.Narrow
	n.Range = rng
	lookup := obsquery.SessionLookup{Scope: localSessionScope(n), SessionID: req.ID}
	lst, err := lookup.Compile()
	if err != nil {
		return GetSessionResult{}, compileErr(err)
	}
	cursor, err := obsquery.DecodeTimelineCursor(req.PageToken)
	if err != nil {
		return GetSessionResult{}, compileErr(err)
	}
	tl := obsquery.SessionTimeline{Scope: lookup.Scope, SessionID: req.ID, Limit: req.Limit, Cursor: cursor}
	if req.Explain {
		// The session is not read, so its span is unknown: compile over the
		// longest window a timeline reads, ending where the lookup ends.
		tl.Scope.Start = rng.To.Add(-obsquery.MaxRangeTimeline)
		tl.Scope.End = rng.To
		tst, err := tl.Compile()
		if err != nil {
			return GetSessionResult{}, compileErr(err)
		}
		return GetSessionResult{Statements: []Statement{
			stmt("session summary", lst),
			stmt("session timeline (over the longest window; a real read narrows it to the session's span)", tst),
		}}, nil
	}
	srows, err := l.rows(ctx, lst)
	if err != nil {
		return GetSessionResult{}, err
	}
	sum, err := obsquery.ParseSession(srows, nil)
	switch {
	case errors.Is(err, obsquery.ErrSessionNotFound):
		return GetSessionResult{}, errSessionNotFound(req.ID)
	case err != nil:
		return GetSessionResult{}, sessionErr(err)
	}
	start := sum.Started.Add(-timelineLead)
	end := sum.Ended.Add(timelineTail)
	if end.Sub(start) > obsquery.MaxRangeTimeline {
		end = start.Add(obsquery.MaxRangeTimeline)
	}
	tl.Scope.Start, tl.Scope.End = start.UTC(), end.UTC()
	tst, err := tl.Compile()
	if err != nil {
		return GetSessionResult{}, compileErr(err)
	}
	res := GetSessionResult{Session: sessionSummary(sum), Statements: []Statement{stmt("session summary", lst), stmt("session timeline", tst)}}
	trows, err := l.rows(ctx, tst)
	if err != nil {
		return res, err
	}
	page, err := tl.Parse(trows)
	if err != nil {
		return res, sessionErr(err)
	}
	res.NextPageToken = page.NextCursor
	for _, e := range page.Entries {
		res.Timeline = append(res.Timeline, SessionEntry{
			Time: e.Time.UTC(), Kind: string(e.Kind), Name: e.Name, Path: e.Path, Method: e.Method, Status: e.Status,
			Selector: e.Selector, Element: e.Element, ErrorType: e.ErrorType, Message: e.Message, Fingerprint: e.Fingerprint,
			TraceID: e.TraceID, SpanID: e.SpanID, DurationMs: e.DurationMs, Props: e.Props, Service: e.Service,
		})
	}
	return res, nil
}
