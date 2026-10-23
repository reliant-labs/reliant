package obsbackend

import (
	"context"
	"slices"
	"strconv"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// Local issues: sfp1 grouping of ERROR logs at query time (DESIGN §5.3). There
// is no store behind them, so there is no state: every issue is unresolved, its
// counts are those of the queried range, and triage has nowhere to be recorded.

// maxEventAttrBytes bounds one attribute value of an event; the stack of a
// recovered panic is the case that needs it.
const maxEventAttrBytes = 512

// errNoLocalTriage is the refusal of every write on the local backend.
func errNoLocalTriage() error {
	return Undetermined("local issues carry no state; triage on a hosted env")
}

func errLocalOnly(what string) error {
	return Undetermined("%s needs a hosted env: the release ledger that answers it lives on the control plane", what)
}

// ListIssues implements IssueReader.
func (l Local) ListIssues(ctx context.Context, req ListIssuesRequest) (ListIssuesResult, error) {
	if req.SinceRelease != "" {
		return ListIssuesResult{}, errLocalOnly("--since-release")
	}
	if req.SincePromotion != "" {
		return ListIssuesResult{}, errLocalOnly("--since-deploy")
	}
	q := obsquery.ServerIssues{Scope: LocalScope(req.Narrow, l.Env), Limit: req.Limit, Sort: req.Sort}
	st, err := q.Compile()
	if err != nil {
		return ListIssuesResult{}, compileErr(err)
	}
	res := ListIssuesResult{Statements: []Statement{stmt("issues", st)}}
	if req.Explain {
		return res, nil
	}
	// Every local issue is unresolved and server-side: a filter for another
	// status or for browser issues matches nothing, truthfully.
	if req.Platform == PlatformBrowser || (len(req.Statuses) > 0 && !slices.Contains(req.Statuses, IssueUnresolved) && !slices.Contains(req.Statuses, IssueNew)) {
		return res, nil
	}
	r, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return res, err
	}
	for _, row := range r.Rows {
		if len(row) < len(obsquery.ServerIssuesColumns) {
			return res, Undetermined("the local ClickStack returned %d columns for issues, want %d", len(row), len(obsquery.ServerIssuesColumns))
		}
		if len(res.Issues) == req.Limit {
			res.Truncated = true
			break
		}
		res.Issues = append(res.Issues, localIssue(row))
	}
	return res, nil
}

func localIssue(row []any) Issue {
	fp := AsString(row[0])
	return Issue{
		ID: obsquery.LocalIssueID(fp), Platform: PlatformServer, Fingerprint: fp, Status: IssueUnresolved,
		Service: AsString(row[1]), Title: AsString(row[2]), Culprit: AsString(row[3]), ExceptionType: AsString(row[4]),
		Events: AsInt(row[5]), Affected: AsInt(row[6]),
		FirstSeen: AsTime(row[7]), LastSeen: AsTime(row[8]),
		LatestTraceID: AsString(row[9]), LastVersion: AsString(row[10]), FirstVersion: AsString(row[11]),
	}
}

func localEvent(row []any) IssueEvent {
	attrs := AsStringMap(row[6])
	for k, v := range attrs {
		if len(v) > maxEventAttrBytes {
			attrs[k] = v[:maxEventAttrBytes]
		}
	}
	return IssueEvent{
		Time: AsTime(row[0]), TraceID: AsString(row[1]), SpanID: AsString(row[2]),
		Version: AsString(row[4]), Message: AsString(row[5]), Attributes: attrs,
	}
}

// GetIssue implements IssueReader: the issue, its newest event, the frame that
// logged it, its per-version counts and the issues sharing its traces.
func (l Local) GetIssue(ctx context.Context, req GetIssueRequest) (GetIssueResult, error) {
	fp, err := obsquery.ParseLocalIssueID(req.ID)
	if err != nil {
		return GetIssueResult{}, compileErr(err)
	}
	scope := LocalScope(req.Narrow, l.Env)
	one, err := obsquery.ServerIssues{Scope: scope, Fingerprint: fp, Limit: 1}.Compile()
	if err != nil {
		return GetIssueResult{}, compileErr(err)
	}
	latest, err := obsquery.ServerIssueEvents{Scope: scope, Fingerprint: fp, Limit: 1}.Compile()
	if err != nil {
		return GetIssueResult{}, compileErr(err)
	}
	versions, err := obsquery.ServerIssueVersions{Scope: scope, Fingerprint: fp}.Compile()
	if err != nil {
		return GetIssueResult{}, compileErr(err)
	}
	co, err := obsquery.ServerIssueCoOccurring{Scope: scope, Fingerprint: fp}.Compile()
	if err != nil {
		return GetIssueResult{}, compileErr(err)
	}
	res := GetIssueResult{Statements: []Statement{
		stmt("issue", one), stmt("latest event", latest), stmt("events per version", versions), stmt("issues sharing its traces", co),
	}}
	if req.Explain {
		return res, nil
	}
	lim := DefaultLimits()
	r, err := l.run(ctx, one, lim)
	if err != nil {
		return res, err
	}
	if len(r.Rows) == 0 {
		return res, Wrong("no issue %s in the range: it has no ERROR log in it; widen --since", req.ID)
	}
	if len(r.Rows[0]) < len(obsquery.ServerIssuesColumns) {
		return res, Undetermined("the local ClickStack returned %d columns for an issue, want %d", len(r.Rows[0]), len(obsquery.ServerIssuesColumns))
	}
	res.Issue = localIssue(r.Rows[0])

	if r, err = l.run(ctx, latest, lim); err != nil {
		return res, err
	}
	if len(r.Rows) > 0 && len(r.Rows[0]) >= 7 {
		ev := localEvent(r.Rows[0])
		res.LatestEvent = &ev
		if f := localFrame(ev.Attributes); f != nil {
			res.Frames = []IssueFrame{*f}
		}
	}
	if r, err = l.run(ctx, versions, lim); err != nil {
		return res, err
	}
	for _, row := range r.Rows {
		if len(row) < 5 {
			continue
		}
		res.ByVersion = append(res.ByVersion, IssueVersion{
			Version: AsString(row[0]), Events: AsInt(row[1]), Affected: AsInt(row[2]),
			FirstSeen: AsTime(row[3]), LastSeen: AsTime(row[4]),
		})
	}
	if r, err = l.run(ctx, co, lim); err != nil {
		return res, err
	}
	for _, row := range r.Rows {
		if len(row) >= 1 {
			res.CoOccurring = append(res.CoOccurring, obsquery.LocalIssueID(AsString(row[0])))
		}
	}
	return res, nil
}

// localFrame is the call site forge/pkg/observe stamps on every ERROR record.
func localFrame(attrs map[string]string) *IssueFrame {
	fn, file := attrs[obsquery.LogKeyCodeFunction], attrs[obsquery.LogKeyCodeFile]
	if fn == "" && file == "" {
		return nil
	}
	line, _ := strconv.Atoi(attrs["code.line.number"])
	return &IssueFrame{Function: fn, File: file, Line: line, InApp: true}
}

// ListIssueEvents implements IssueReader.
func (l Local) ListIssueEvents(ctx context.Context, req ListIssueEventsRequest) (ListIssueEventsResult, error) {
	fp, err := obsquery.ParseLocalIssueID(req.ID)
	if err != nil {
		return ListIssueEventsResult{}, compileErr(err)
	}
	st, err := obsquery.ServerIssueEvents{Scope: LocalScope(req.Narrow, l.Env), Fingerprint: fp, Limit: req.Limit}.Compile()
	if err != nil {
		return ListIssueEventsResult{}, compileErr(err)
	}
	res := ListIssueEventsResult{Statements: []Statement{stmt("issue events", st)}}
	if req.Explain {
		return res, nil
	}
	r, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return res, err
	}
	for _, row := range r.Rows {
		if len(row) < 7 {
			return res, Undetermined("the local ClickStack returned %d columns for issue events, want 7", len(row))
		}
		if len(res.Events) == req.Limit {
			res.Truncated = true
			break
		}
		res.Events = append(res.Events, localEvent(row))
	}
	return res, nil
}

// UpdateIssueStatus implements IssueReader: local issues carry no state.
func (l Local) UpdateIssueStatus(context.Context, UpdateIssueStatusRequest) (UpdateIssueStatusResult, error) {
	return UpdateIssueStatusResult{}, errNoLocalTriage()
}
