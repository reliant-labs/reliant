package obsquery

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Browser panel kinds: funnel, issues and sessions. Unlike number, line and
// table they are not series over a signal. Each reads one of the control
// plane's per-org browser tables (its obstenant schema, migration 0003; absent
// from a local ClickStack) and returns a
// fixed shape:
//
//	funnel:   one row, one UInt64 per step: entrants that reached that step
//	          and every step before it (ClickHouse windowFunnel over product_events)
//	issues:   one row per distinct stack (error_hourly), enriched with the most
//	          recent event of that stack (error_events); at most MaxPanelIssues+1
//	sessions: one row per browser session that started in the range
//	          (rum_sessions, merged per session); at most MaxPanelSessions+1
//
// Every statement is bounded by the scope exactly as a traces panel is: the
// environments the caller may see, the services and the attribute filters.

// Browser panel limits.
const (
	MaxRangeFunnel   = 90 * 24 * time.Hour // product_events retention
	MaxRangeIssues   = 30 * 24 * time.Hour // error_hourly retention
	MaxRangeSessions = 30 * 24 * time.Hour // rum_sessions retention (replay days, capped at 30)

	MaxPanelIssues   = 50
	MaxPanelSessions = 50

	MinFunnelSteps      = 2
	MaxFunnelSteps      = 10
	maxFunnelStepLen    = 128
	DefaultFunnelWindow = "1h"
	DefaultFunnelBy     = "session"
)

// PanelFunnel is the definition of a funnel panel (forge.Funnel).
type PanelFunnel struct {
	Steps  []string
	Window string // "<n><s|m|h|d>", at most MaxRangeFunnel; empty = DefaultFunnelWindow
	By     string // "session" | "user"; empty = DefaultFunnelBy
}

// IsBrowserKind reports whether kind is read from the browser tables.
func IsBrowserKind(kind string) bool {
	switch kind {
	case "funnel", "issues", "sessions":
		return true
	}
	return false
}

// MaxRange is the maximum range of a panel: by kind for the browser kinds, by
// signal for the rest.
func (p Panel) MaxRange() (time.Duration, error) {
	switch p.Kind {
	case "funnel":
		return MaxRangeFunnel, nil
	case "issues":
		return MaxRangeIssues, nil
	case "sessions":
		return MaxRangeSessions, nil
	case "sql":
		return MaxRangeDashboards, nil
	}
	return PanelMaxRange(p.Signal)
}

var funnelWindowRe = regexp.MustCompile(`^([1-9][0-9]{0,3})([smhd])$`)

// FunnelWindow resolves a funnel window to a duration.
func FunnelWindow(w string) (time.Duration, error) {
	if w == "" {
		w = DefaultFunnelWindow
	}
	m := funnelWindowRe.FindStringSubmatch(w)
	if m == nil {
		return 0, invalid("funnel window %q is not a number and a unit of s, m, h or d, like \"1h\" or \"7d\"", w)
	}
	n, _ := strconv.Atoi(m[1])
	unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
	d := time.Duration(n) * unit
	if d > MaxRangeFunnel {
		return 0, invalid("funnel window %q is longer than %s", w, MaxRangeFunnel)
	}
	return d, nil
}

// validateBrowser checks a funnel, issues or sessions panel.
func (p Panel) validateBrowser() error {
	if p.Signal != "" && p.Signal != "traces" {
		return invalid("a %s panel reads browser telemetry: its signal is traces, got %q", p.Kind, p.Signal)
	}
	if len(p.Series) > 0 || p.Ratio || len(p.GroupBy) > 0 || p.Formula != "" || p.Limit != 0 || p.SQL != "" {
		return invalid("a %s panel takes no series, ratio, formula, limit, group_by or sql", p.Kind)
	}
	if len(p.Where) > MaxPanelPredicates {
		return invalid("at most %d panel predicates", MaxPanelPredicates)
	}
	if p.HasError && p.Kind != "sessions" {
		return invalid("only a sessions panel takes has_error")
	}
	if p.Kind != "funnel" {
		if p.Funnel != nil {
			return invalid("only a funnel panel takes a funnel")
		}
		return nil
	}
	f := p.Funnel
	if f == nil {
		return invalid("a funnel panel needs a funnel")
	}
	if len(f.Steps) < MinFunnelSteps || len(f.Steps) > MaxFunnelSteps {
		return invalid("a funnel has %d to %d steps, has %d", MinFunnelSteps, MaxFunnelSteps, len(f.Steps))
	}
	seen := map[string]bool{}
	for _, s := range f.Steps {
		switch {
		case strings.TrimSpace(s) == "":
			return invalid("a funnel step must not be empty")
		case len(s) > maxFunnelStepLen:
			return invalid("a funnel step is longer than %d characters", maxFunnelStepLen)
		case seen[s]:
			return invalid("funnel step %q repeats an earlier step", s)
		}
		seen[s] = true
	}
	if _, err := FunnelWindow(f.Window); err != nil {
		return err
	}
	switch f.By {
	case "", "session", "user":
	default:
		return invalid("funnel by must be session or user, got %q", f.By)
	}
	return nil
}

// ── Field resolution over the browser tables ────────────────────────────

const (
	tblProduct  = "product_events"
	tblIssues   = "error_hourly"
	tblSessions = "rum_sessions"
	browserSig  = "browser:"
)

// browserEnvName is deployment.environment.name for a browser table: the
// Reliant environment's name, resolved from the row's EnvId.
func (b *builder) browserEnvName(idExpr string) string {
	if len(b.envs) == 0 {
		return "''"
	}
	ids := make([]string, len(b.envs))
	names := make([]string, len(b.envs))
	for i, e := range b.envs {
		ids[i], names[i] = e.ID, e.Name
	}
	return "transform(" + idExpr + ", " + b.strArray(ids) + ", " + b.strArray(names) + ", '')"
}

// postMerge reports whether a field of rum_sessions only has a value once the
// session's partial rows are merged. A session has one row per day from each of
// two views (spans, replay), and the replay view carries no user, so filtering
// these per row would drop the replay-only rows and lose HasReplay.
func postMerge(table, field string) bool {
	if table != tblSessions {
		return false
	}
	switch field {
	case "service.version", "reliant.env.id", "deployment.environment.name", "span.attr.userId":
		return true
	}
	return false
}

// resolveBrowser resolves a panel field to an expression over one browser table.
func (b *builder) resolveBrowser(table, field string) (fieldRef, error) {
	merged := func(col string) string {
		if table == tblSessions {
			return "max(" + col + ")"
		}
		return col
	}
	switch field {
	case "service.name":
		return fieldRef{expr: "ServiceName"}, nil
	case "service.version":
		return fieldRef{expr: merged("ServiceVersion")}, nil
	case "reliant.env.id":
		return fieldRef{expr: merged("EnvId")}, nil
	case "deployment.environment.name":
		return fieldRef{expr: b.browserEnvName(merged("EnvId"))}, nil
	case "resource.attr.rum.sessionId":
		if table != tblIssues {
			return fieldRef{expr: "SessionId"}, nil
		}
	case "span.attr.userId":
		if table != tblIssues {
			return fieldRef{expr: merged("UserId")}, nil
		}
	}
	if table == tblProduct && strings.HasPrefix(field, "span.attr.") {
		k, err := attrKey(field, "span.attr.")
		if err != nil {
			return fieldRef{}, err
		}
		return b.mapRef("Props", k), nil
	}
	return fieldRef{}, invalid("field %q is not available on a panel that reads %s", field, table)
}

// filterPredicate expresses a scope attribute filter as a panel predicate.
func filterPredicate(f AttrFilter) (PanelPredicate, error) {
	switch f.Op {
	case "", "eq":
		return PanelPredicate{Field: f.Key, Op: "eq", Value: f.Value}, nil
	case "neq", "in", "exists":
		return PanelPredicate{Field: f.Key, Op: f.Op, Value: f.Value, Values: f.Values}, nil
	case "contains":
		if f.Value == "" {
			return PanelPredicate{}, invalid("a contains filter needs a value")
		}
		return PanelPredicate{Field: f.Key, Op: "match", Value: "(?i)" + regexp.QuoteMeta(f.Value)}, nil
	}
	return PanelPredicate{}, invalid("unknown filter operator %q", f.Op)
}

// browserEnvCond restricts idExpr to the scope's environments ("" when the
// caller sees every environment).
func (b *builder) browserEnvCond(idExpr string, s Scope) string {
	if !s.EnvRestricted {
		return ""
	}
	switch len(s.EnvIDs) {
	case 0:
		return "1 = 0"
	case 1:
		return idExpr + " = " + b.str(s.EnvIDs[0])
	}
	return "has(" + b.strArray(s.EnvIDs) + ", " + idExpr + ")"
}

// browserConds compiles everything that narrows a read of one browser table:
// the scope (environments, services, attribute filters), the panel's own
// predicates and the request's filters. where applies per row; having applies
// after rum_sessions rows are merged per session.
func (b *builder) browserConds(table string, q PanelQuery) (where, having []string, err error) {
	sc := q.Scope
	if sv := b.serviceIn(sc.Services); sv != "" {
		where = append(where, sv)
	}
	if table == tblSessions {
		if e := b.browserEnvCond("max(EnvId)", sc); e != "" {
			having = append(having, e)
		}
	} else if e := b.browserEnvCond("EnvId", sc); e != "" {
		where = append(where, e)
	}
	preds := make([]PanelPredicate, 0, len(sc.Filters)+len(q.Panel.Where)+len(q.Filters))
	for _, f := range sc.Filters {
		p, err := filterPredicate(f)
		if err != nil {
			return nil, nil, err
		}
		preds = append(preds, p)
	}
	preds = append(append(preds, q.Panel.Where...), q.Filters...)
	for _, p := range preds {
		// Every error row has a session (the view that makes them requires one),
		// so "has a session" holds for any issue by construction.
		if table == tblIssues && p.Field == "resource.attr.rum.sessionId" {
			if p.Op != "exists" {
				return nil, nil, invalid("an issues panel can only test that a session exists, not its value")
			}
			continue
		}
		c, err := b.cond(browserSig+table, p)
		if err != nil {
			return nil, nil, err
		}
		if postMerge(table, p.Field) {
			having = append(having, c)
		} else {
			where = append(where, c)
		}
	}
	return where, having, nil
}

func (b *builder) u64(v uint64) string { return b.add("UInt64", strconv.FormatUint(v, 10)) }

// ── Compilation ─────────────────────────────────────────────────────────

func (q PanelQuery) compileBrowser() (Stmt, PanelShape, error) {
	var shape PanelShape
	max, _ := q.Panel.MaxRange()
	if err := q.Scope.validate(max); err != nil {
		return Stmt{}, shape, err
	}
	if len(q.Filters) > MaxPanelPredicates {
		return Stmt{}, shape, invalid("at most %d filters", MaxPanelPredicates)
	}
	b := newBuilder(q.Scope)
	switch q.Panel.Kind {
	case "funnel":
		return q.compileFunnel(b)
	case "issues":
		return q.compileIssues(b)
	}
	return q.compileSessions(b)
}

// compileFunnel:
//
//	SELECT countIf(level >= 1), ..., countIf(level >= N)   -- one UInt64 per step
//	FROM (SELECT windowFunnel(window_ms)(ts_ms, EventName = s1, ...) AS level
//	      FROM product_events WHERE ... GROUP BY <SessionId | UserId>)
//
// A journey is the events of one session (or one user, across sessions) in the
// range; an event with no session / no user is skipped. windowFunnel counts the
// longest chain of steps in order within the window, so a journey that did step
// 1 then 3 stops at 1.
func (q PanelQuery) compileFunnel(b *builder) (Stmt, PanelShape, error) {
	f := q.Panel.Funnel
	window, err := FunnelWindow(f.Window)
	if err != nil {
		return Stmt{}, PanelShape{}, err
	}
	key := "SessionId"
	if f.By == "user" {
		key = "UserId"
	}
	where := []string{b.rangeCond("Timestamp", q.Scope), key + " != ''"}
	in, err := b.strList(f.Steps, false)
	if err != nil {
		return Stmt{}, PanelShape{}, err
	}
	where = append(where, "EventName IN ("+in+")")
	pw, _, err := b.browserConds(tblProduct, q)
	if err != nil {
		return Stmt{}, PanelShape{}, err
	}
	where = append(where, pw...)

	steps := make([]string, len(f.Steps))
	counts := make([]string, len(f.Steps))
	for i, s := range f.Steps {
		steps[i] = "EventName = " + b.str(s)
		counts[i] = "countIf(level >= " + strconv.Itoa(i+1) + ") AS s" + strconv.Itoa(i+1)
	}
	sql := "SELECT " + strings.Join(counts, ", ") + " FROM (\n" +
		"  SELECT windowFunnel(" + b.u64(uint64(window/time.Millisecond)) + ")(toUInt64(toUnixTimestamp64Milli(Timestamp)), " + strings.Join(steps, ", ") + ") AS level\n" +
		"  FROM " + b.table(q.Scope, tblProduct) + "\n  " + where2(where) + "\n  GROUP BY " + key + "\n)"
	return b.stmt(sql), PanelShape{Series: len(f.Steps)}, nil
}

// where2 is where() indented to sit inside a subquery.
func where2(conds []string) string { return where(conds) }

// hourBounds widens a range to whole hours: error_hourly is hourly, and the
// sample events must come from the same window as the counts they illustrate.
func hourBounds(s Scope) Scope {
	s.Start = s.Start.UTC().Truncate(time.Hour)
	if e := s.End.UTC().Truncate(time.Hour); e.Before(s.End) {
		s.End = e.Add(time.Hour)
	} else {
		s.End = e
	}
	return s
}

// compileIssues groups errors by distinct stack. Today an issue IS a stack
// (RawStackHash, per service): the same bug shipped in two builds has two
// stacks until the enrichment worker fingerprints across builds.
//
// SEAM (C5, frontend_issues): once issue state lives in Postgres, join it in the
// handler, not here. This statement keeps returning (ServiceName, RawStackHash)
// as the key so a status, an assignee or a merged-into issue can be looked up
// per row, and a fingerprint from error_stacks can replace the key by joining
// error_stacks on (ServiceName, ServiceVersion, RawStackHash). Status filters
// ("new", "regressed", "resolved") need that join BEFORE the LIMIT below, so
// they belong in a second statement over a candidate set, not in this one.
//
// Counts are at hour granularity (error_hourly), over the hours the range touches.
func (q PanelQuery) compileIssues(b *builder) (Stmt, PanelShape, error) {
	w := hourBounds(q.Scope)
	hw, _, err := b.browserConds(tblIssues, q)
	if err != nil {
		return Stmt{}, PanelShape{}, err
	}
	hourly := append([]string{b.rangeCondSec("Hour", w)}, hw...)
	// The same conditions read error_events: its columns of those names match.
	events := append([]string{b.rangeCond("Timestamp", w)}, hw...)
	events = append(events, "(ServiceName, RawStackHash) IN (SELECT ServiceName, RawStackHash FROM agg)")

	sql := "WITH agg AS (\n" +
		"  SELECT ServiceName, RawStackHash, sum(Events) AS events, uniqMerge(Sessions) AS sessions,\n" +
		"         min(FirstSeen) AS first_seen, max(LastSeen) AS last_seen\n" +
		"  FROM " + b.table(q.Scope, tblIssues) + "\n  " + where(hourly) + "\n" +
		"  GROUP BY ServiceName, RawStackHash\n" +
		"  ORDER BY events DESC, last_seen DESC, ServiceName, RawStackHash\n" +
		"  LIMIT " + strconv.Itoa(MaxPanelIssues+1) + "\n)\n" +
		"SELECT ServiceName, toString(RawStackHash), events, sessions, first_seen, last_seen,\n" +
		"       ifNull(etype, ''), ifNull(msg, ''), ifNull(url_path, ''), ifNull(version, ''), ifNull(trace_id, ''), ifNull(session_id, '')\n" +
		"FROM agg\nLEFT JOIN (\n" +
		"  SELECT ServiceName, RawStackHash, argMax(ExceptionType, Timestamp) AS etype, argMax(MessageNorm, Timestamp) AS msg,\n" +
		"         argMax(UrlPath, Timestamp) AS url_path, argMax(ServiceVersion, Timestamp) AS version,\n" +
		"         argMax(TraceId, Timestamp) AS trace_id, argMax(SessionId, Timestamp) AS session_id\n" +
		"  FROM " + b.table(q.Scope, "error_events") + "\n  " + where(events) + "\n" +
		"  GROUP BY ServiceName, RawStackHash\n) AS e USING (ServiceName, RawStackHash)\n" +
		"ORDER BY events DESC, last_seen DESC, ServiceName, RawStackHash"
	return b.stmt(sql), PanelShape{}, nil
}

// compileSessions lists the sessions that STARTED in the range, newest first.
// A session crossing midnight has rows on two days, and each day has a row from
// the span view and from the replay view, so the read merges them per
// (ServiceName, SessionId). The day before the range is read too: a session
// that began there must not look as if it began at the range's first midnight.
func (q PanelQuery) compileSessions(b *builder) (Stmt, PanelShape, error) {
	sw, having, err := b.browserConds(tblSessions, q)
	if err != nil {
		return Stmt{}, PanelShape{}, err
	}
	days := []string{
		"Day >= subtractDays(toDate(" + b.nanoTime(q.Scope.Start) + "), 1)",
		"Day <= toDate(" + b.nanoTime(q.Scope.End) + ")",
	}
	having = append([]string{b.rangeCond("started", q.Scope)}, having...)
	if q.Panel.HasError {
		having = append(having, "sum(Errors) > 0")
	}
	sql := "SELECT SessionId, ServiceName, max(ServiceVersion) AS version, max(EnvId) AS env_id, max(UserId) AS user_id,\n" +
		"       argMinMerge(EntryPath) AS entry_path, min(Start) AS started, max(End) AS ended,\n" +
		"       sum(PageViews) AS page_views, sum(Interactions) AS interactions, sum(Errors) AS errors, max(HasReplay) AS has_replay\n" +
		"FROM " + b.table(q.Scope, tblSessions) + "\n" + where(append(days, sw...)) + "\n" +
		"GROUP BY ServiceName, SessionId\n" +
		"HAVING " + strings.Join(having, "\n  AND ") + "\n" +
		"ORDER BY started DESC, ServiceName, SessionId\nLIMIT " + strconv.Itoa(MaxPanelSessions+1)
	return b.stmt(sql), PanelShape{}, nil
}
