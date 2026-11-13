package obsquery

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// User activity: how many users an environment had, read from the per-org
// product_events table — active, new and returning over a range and over the
// range of the same length before it, and per UTC day. `forge obs users`, the
// control plane's GetUserActivity and the generated dashboards all compile
// from the two exported fragments below, so the Overview's Users card and the
// dashboards cannot disagree:
//
//   - UserActivityEvidence: the rows that count a user as active. Any
//     user-attributed product event: the standard auth events (a sign-in, the
//     auth middleware's daily resumed heartbeat), the automatic CRUD events,
//     the app's own. "Signed in during the range" is the meaning; every
//     event a signed-in user caused is the evidence. An event that is a
//     fact about the user rather than something they did
//     (observe.TrackState) is not, and a project's declared rules
//     (Activity: events that are not activity, markers whose users are left
//     out) narrow it further through ActiveEvidenceFor.
//   - NewUsersSource: each user's "new" moment. An environment that reports
//     sign-ups (an auth_signed_up in the NewUsersWindowDays before the end
//     of the range) counts the users who signed up; one that does not counts
//     the users first seen in that window, except those first seen within
//     FirstSeenGraceDays of the earliest event the window holds: there the
//     data, not the user, is new.
//   - NewUsersDecidedFrom: per environment, the moment from which
//     NewUsersSource can tell a new user from a returning one, so a reader
//     can say a range it cannot decide is undecided instead of guessing.
//
// returning = active users who are not new. Every count is distinct by
// UserId, so a duplicate event never inflates one. Like the session reads
// this is pure: Compile builds a Stmt, Parse reads the rows, and
// ReadUserActivity runs both through the caller's Runner.

// User-activity limits.
const (
	// MaxRangeUserActivity is the longest range: product_events' default
	// retention.
	MaxRangeUserActivity = MaxRangeFunnel
	// MaxUserActivitySeriesDays is the longest daily series.
	MaxUserActivitySeriesDays = 90
	// NewUsersWindowDays is how far before the end of a range NewUsersSource
	// looks: for whether the environment reports sign-ups, and for a user's
	// first event.
	NewUsersWindowDays = 90
	// FirstSeenGraceDays is how long after the earliest event a first-seen
	// read holds (where the retained data begins, or where the window does)
	// a user's first event still says nothing about whether they are new: a
	// user who comes back weekly is "first seen" within a week of wherever
	// the data starts. NewUsersSource counts none of those users new.
	FirstSeenGraceDays = 7
	// MaxSeenTotalDays bounds the retention-wide distinct count:
	// product_events' longest retention.
	MaxSeenTotalDays = 400
)

// EventAuthAccounts is the control plane's daily per-env account snapshot, a
// platform event that names no user's activity.
const EventAuthAccounts = "auth_accounts"

// UserActivityEvidence is the predicate on product_events rows that count their
// UserId as active: any user-attributed event except a sign-out (which ends a
// session), the platform's account snapshot, and an event that is a fact
// about the user rather than something they did (observe.TrackState's
// observe.PropKind = observe.KindState: a billing snapshot, a staff marker).
// A project's own rules extend it (ActiveEvidenceFor).
const UserActivityEvidence = "UserId != '' AND EventName NOT IN ('" + string(observe.AuthSignedOut) + "', '" + EventAuthAccounts + "')" +
	" AND Props['" + observe.PropKind + "'] != '" + observe.KindState + "'"

// ActiveEvidence is the predicate UserActivityEvidence was before
// observe.TrackState: any user-attributed event except a sign-out and the
// platform's account snapshot. Its value is frozen (forge is semver at major
// 1, and a caller may compare or embed it); it still counts a TrackState fact
// as activity.
//
// Deprecated: use UserActivityEvidence, which leaves TrackState facts out.
const ActiveEvidence = "UserId != '' AND EventName NOT IN ('" + string(observe.AuthSignedOut) + "', '" + EventAuthAccounts + "')"

// NewUsersSource is a subquery over table with one row per new user of the
// window that ends at to (a DateTime64(9) SQL expression):
//
//	EnvId, UserId, NewAt DateTime64(9), ReportsSignups UInt8
//
// An environment with any auth_signed_up in the NewUsersWindowDays before to
// "reports sign-ups" (ReportsSignups = 1): its new users are the ones that
// signed up, NewAt their first sign-up. Otherwise a user first seen
// (UserActivityEvidence) in that window is new, NewAt their first event, unless
// that event is within FirstSeenGraceDays of the environment's earliest event
// in the window: the retained data begins there (or the window does), so a
// user first seen then may well have been active before it. Such a user is
// not new, and NewUsersDecidedFrom says from when the environment's new users
// are decided. scope is an extra predicate on the rows ("" for none): the
// environments and services of the read. A caller counts the users with
// NewAt in its range.
func NewUsersSource(table, to, scope string) string {
	return newUsersSource(table, to, scope, UserActivityEvidence)
}

// newUsersSource is NewUsersSource with evidence as the rows that count a user
// (UserActivityEvidence, or ActiveEvidenceFor a project's rules).
func newUsersSource(table, to, scope, evidence string) string {
	return "SELECT EnvId, UserId, if(reports = 1, signed_up_at, first_at) AS NewAt, reports AS ReportsSignups\n" +
		"FROM (\n" +
		"  SELECT EnvId, UserId, first_at, signed_up_at, has_signup, max(has_signup) OVER (PARTITION BY EnvId) AS reports,\n" +
		"         min(first_at) OVER (PARTITION BY EnvId) AS data_from\n" +
		"  FROM (\n" + newUsersPerUser(table, to, scope, evidence) + "))\n" +
		"WHERE has_signup = 1 OR (reports = 0 AND first_at >= data_from + INTERVAL " + strconv.Itoa(FirstSeenGraceDays) + " DAY)"
}

// NewUsersDecidedFrom is a subquery over table with one row per environment
// with any UserActivityEvidence row in the NewUsersWindowDays before to (a
// DateTime64(9) SQL expression):
//
//	EnvId, ReportsSignups UInt8, DataFrom DateTime64(9), DecidedFrom DateTime64(9)
//
// DataFrom is the environment's earliest such row. DecidedFrom is the moment
// from which NewUsersSource tells a new user from a returning one: the epoch
// when the environment reports sign-ups (they decide it), else DataFrom plus
// FirstSeenGraceDays. Before DecidedFrom a user's first event cannot say
// whether they are new, so NewUsersSource counts them returning; a range that
// starts before DecidedFrom is only partly decided, and one that ends at or
// before it is not decided at all. scope is NewUsersSource's.
func NewUsersDecidedFrom(table, to, scope string) string {
	return newUsersDecidedFrom(table, to, scope, UserActivityEvidence)
}

// newUsersDecidedFrom is NewUsersDecidedFrom over evidence (NewUsersSource's).
func newUsersDecidedFrom(table, to, scope, evidence string) string {
	return "SELECT EnvId, toUInt8(max(has_signup)) AS ReportsSignups, min(first_at) AS DataFrom,\n" +
		"       if(max(has_signup) = 1, toDateTime64(0, 9), min(first_at) + INTERVAL " + strconv.Itoa(FirstSeenGraceDays) + " DAY) AS DecidedFrom\n" +
		"FROM (\n" + newUsersPerUser(table, to, scope, evidence) + ")\n" +
		"GROUP BY EnvId"
}

// newUsersPerUser is the read NewUsersSource and NewUsersDecidedFrom share:
// per user of the window, their first event, their first sign-up and whether
// they signed up.
func newUsersPerUser(table, to, scope, evidence string) string {
	if scope != "" {
		scope = " AND " + scope
	}
	signup := "EventName = '" + string(observe.AuthSignedUp) + "'"
	return "    SELECT EnvId, UserId, min(Timestamp) AS first_at, minIf(Timestamp, " + signup + ") AS signed_up_at,\n" +
		"           toUInt8(countIf(" + signup + ") > 0) AS has_signup\n" +
		"    FROM " + table + "\n" +
		"    WHERE " + evidence + " AND Timestamp >= " + to + " - INTERVAL " + strconv.Itoa(NewUsersWindowDays) + " DAY AND Timestamp < " + to + scope + "\n" +
		"    GROUP BY EnvId, UserId"
}

// NewUsersRule says how an environment's new users were counted.
type NewUsersRule string

const (
	// NewUsersSignups: the users with an auth_signed_up in the range.
	NewUsersSignups NewUsersRule = "signups"
	// NewUsersFirstSeen: the environment reports no sign-ups, so the users
	// first seen in the range (within NewUsersWindowDays before its end) and
	// after its NewDecidedFrom.
	NewUsersFirstSeen NewUsersRule = "first_seen"
)

// UserActivityCounts are the distinct users of one window.
type UserActivityCounts struct {
	Active, New, Returning int64
}

// UserActivityDay is one UTC day of the series.
type UserActivityDay struct {
	Day string // YYYY-MM-DD, UTC
	UserActivityCounts
}

// EnvUserActivity is one environment's users.
type EnvUserActivity struct {
	EnvID, EnvName string
	// Current is the query's range; Previous the range of the same length
	// before it.
	Current, Previous UserActivityCounts
	// Days is the series, oldest first, zero-filled; empty without SeriesDays.
	Days []UserActivityDay
	// SeenTotal is the distinct users with any UserActivityEvidence row still
	// retained (at most MaxSeenTotalDays); nil unless WantSeenTotal.
	SeenTotal *int64
	// RetentionDays is the longest retention of the rows read (what SeenTotal
	// spans); 0 when none was read.
	RetentionDays uint32
	// FirstEventAt is the earliest UserActivityEvidence row read: within the read
	// window, or within retention with WantSeenTotal. Zero: none.
	FirstEventAt time.Time
	// Clients are where the current window's rows came from (app.client:
	// "backend", "web", …), sorted.
	Clients []string
	// NewRule is how New was counted.
	NewRule NewUsersRule
	// NewDecidedFrom is, under NewUsersFirstSeen, the moment from which
	// Current's New tells a new user from a returning one
	// (NewUsersDecidedFrom): a user first seen before it is counted
	// returning, because the data, not the user, begins there. Zero when
	// sign-ups decide New, or the window holds no rows. NewDecided reads it
	// against the window ([Scope.Start, Scope.End)).
	NewDecidedFrom time.Time
	// PreviousNewDecidedFrom is NewDecidedFrom for Previous (the window of
	// the range's length before Scope.Start).
	PreviousNewDecidedFrom time.Time
}

// NewDecision says how much of a window New decides.
type NewDecision string

const (
	// NewDecidedAll: every user of the window is told new or returning.
	NewDecidedAll NewDecision = "all"
	// NewDecidedPart: the window starts before its decided-from moment, so
	// a user first seen before that moment is counted returning though they
	// may be new. New is a lower bound and Returning an upper one.
	NewDecidedPart NewDecision = "part"
	// NewDecidedNone: the window ends at or before its decided-from moment.
	// New (0) and Returning (= Active) say nothing; show neither.
	NewDecidedNone NewDecision = "none"
)

// NewDecided says how much of the window [from, to) a decided-from moment
// (NewDecidedFrom for Current, PreviousNewDecidedFrom for Previous) decides.
func NewDecided(decidedFrom, from, to time.Time) NewDecision {
	switch {
	case decidedFrom.IsZero() || !decidedFrom.After(from):
		return NewDecidedAll
	case decidedFrom.Before(to):
		return NewDecidedPart
	}
	return NewDecidedNone
}

// UserActivityQuery reads the users of the environments in Scope over
// Scope's range (at most MaxRangeUserActivity).
type UserActivityQuery struct {
	Scope Scope
	// SeriesDays is the daily series' length in UTC days, ending with the day
	// of Scope.End; 0 = no series.
	SeriesDays uint32
	// WantSeenTotal adds SeenTotal, the one read over the whole retention.
	WantSeenTotal bool
	// Activity is the project's declared rules (ActiveEvidenceFor); zero is
	// UserActivityEvidence alone.
	Activity Activity
}

// window is the query's derived time bounds.
type userActivityWindow struct {
	prevFrom, curFrom, to, seriesFrom, readFrom time.Time
	seriesDays                                  int
}

func (q UserActivityQuery) window() userActivityWindow {
	d := q.Scope.End.Sub(q.Scope.Start)
	w := userActivityWindow{prevFrom: q.Scope.Start.Add(-d), curFrom: q.Scope.Start, to: q.Scope.End, seriesDays: int(q.SeriesDays)}
	w.readFrom = w.prevFrom
	if w.seriesDays > 0 {
		last := q.Scope.End.Add(-time.Nanosecond).UTC().Truncate(24 * time.Hour)
		w.seriesFrom = last.AddDate(0, 0, -(w.seriesDays - 1))
		if w.seriesFrom.Before(w.readFrom) {
			w.readFrom = w.seriesFrom
		}
	}
	return w
}

// Compile builds the statement: one UNION ALL whose rows are
//
//	kind, env_id, day, active_users, new_users, returning_users, clients, first_at, retention_days, flag
//
// with kind cur and prev (the windows), day (the series), env (clients, the
// first event and retention), rule and rule_prev (flag: the env reports
// sign-ups; first_at: its NewUsersDecidedFrom, for each window) and seen
// (active_users: SeenTotal).
func (q UserActivityQuery) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeUserActivity); err != nil {
		return Stmt{}, err
	}
	if q.SeriesDays > MaxUserActivitySeriesDays {
		return Stmt{}, invalid("series_days must be at most %d", MaxUserActivitySeriesDays)
	}
	if err := q.Activity.Validate(); err != nil {
		return Stmt{}, err
	}
	s := q.Scope
	w := q.window()
	b := newBuilder(s)
	var scope []string
	if e := b.browserEnvCond("EnvId", s); e != "" {
		scope = append(scope, e)
	}
	if sv := b.serviceIn(s.Services); sv != "" {
		scope = append(scope, sv)
	}
	if e := b.browserInstanceCond(s); e != "" {
		scope = append(scope, e)
	}
	scopeSQL := strings.Join(scope, " AND ")
	and := func(c string) string {
		if c == "" {
			return ""
		}
		return " AND " + c
	}
	table := b.table(s, tblProduct)
	evidence, err := ActiveEvidenceFor(table, q.Activity)
	if err != nil {
		return Stmt{}, err
	}
	to, curFrom, prevFrom, readFrom := b.nanoTime(w.to), b.nanoTime(w.curFrom), b.nanoTime(w.prevFrom), b.nanoTime(w.readFrom)
	nuCur, err := NewUsersSourceFor(table, to, scopeSQL, q.Activity)
	if err != nil {
		return Stmt{}, err
	}
	nuPrev, err := NewUsersSourceFor(table, curFrom, scopeSQL, q.Activity)
	if err != nil {
		return Stmt{}, err
	}

	const zeros = "toUInt64(0), toUInt64(0), toUInt64(0)"
	const noMeta = "CAST([] AS Array(String)), toDateTime64(0, 9), toUInt32(0), toUInt8(0)"
	windowPart := func(kind, from, until, src string) string {
		return "SELECT '" + kind + "' AS kind, CAST(EnvId AS String) AS env_id, '' AS day,\n" +
			"       countIf(a = 1) AS active_users, countIf(n = 1) AS new_users, countIf(a = 1 AND n = 0) AS returning_users,\n" +
			"       " + noMeta + "\n" +
			"FROM (\n" +
			"  SELECT EnvId, UserId, max(src = 'a') AS a, max(src = 'n') AS n\n" +
			"  FROM (\n" +
			"    SELECT EnvId, UserId, 'a' AS src FROM act WHERE Timestamp >= " + from + " AND Timestamp < " + until + "\n" +
			"    UNION ALL\n" +
			"    SELECT EnvId, UserId, 'n' AS src FROM " + src + " WHERE NewAt >= " + from + " AND NewAt < " + until + ")\n" +
			"  GROUP BY EnvId, UserId)\n" +
			"GROUP BY EnvId"
	}
	parts := []string{
		windowPart("cur", curFrom, to, "nu_cur"),
		windowPart("prev", prevFrom, curFrom, "nu_prev"),
	}
	if w.seriesDays > 0 {
		seriesFrom := b.nanoTime(w.seriesFrom)
		parts = append(parts, "SELECT 'day' AS kind, CAST(EnvId AS String) AS env_id, toString(d) AS day,\n"+
			"       countIf(a = 1) AS active_users, countIf(n = 1) AS new_users, countIf(a = 1 AND n = 0) AS returning_users,\n"+
			"       "+noMeta+"\n"+
			"FROM (\n"+
			"  SELECT EnvId, d, UserId, max(src = 'a') AS a, max(src = 'n') AS n\n"+
			"  FROM (\n"+
			"    SELECT EnvId, toDate(Timestamp, 'UTC') AS d, UserId, 'a' AS src FROM act WHERE Timestamp >= "+seriesFrom+"\n"+
			"    UNION ALL\n"+
			"    SELECT EnvId, toDate(NewAt, 'UTC') AS d, UserId, 'n' AS src FROM nu_cur WHERE NewAt >= "+seriesFrom+")\n"+
			"  GROUP BY EnvId, d, UserId)\n"+
			"GROUP BY EnvId, d")
	}
	parts = append(parts,
		"SELECT 'env' AS kind, CAST(EnvId AS String) AS env_id, '' AS day, "+zeros+",\n"+
			"       arraySort(groupUniqArrayIf(CAST(Client AS String), Timestamp >= "+curFrom+")) AS clients, min(Timestamp) AS first_at,\n"+
			"       toUInt32(max(RetentionDays)) AS retention_days, toUInt8(0) AS flag\n"+
			"FROM act GROUP BY EnvId",
		"SELECT 'rule' AS kind, CAST(EnvId AS String) AS env_id, '' AS day, "+zeros+",\n"+
			"       CAST([] AS Array(String)), DecidedFrom, toUInt32(0), ReportsSignups AS flag\n"+
			"FROM nd_cur",
		"SELECT 'rule_prev' AS kind, CAST(EnvId AS String) AS env_id, '' AS day, "+zeros+",\n"+
			"       CAST([] AS Array(String)), DecidedFrom, toUInt32(0), ReportsSignups AS flag\n"+
			"FROM nd_prev")
	if q.WantSeenTotal {
		seenFrom := b.nanoTime(w.to.Add(-MaxSeenTotalDays * 24 * time.Hour))
		parts = append(parts, "SELECT 'seen' AS kind, CAST(EnvId AS String) AS env_id, '' AS day,\n"+
			"       toUInt64(uniqExact(UserId)) AS active_users, toUInt64(0), toUInt64(0),\n"+
			"       CAST([] AS Array(String)), min(Timestamp) AS first_at, toUInt32(max(RetentionDays)) AS retention_days, toUInt8(0)\n"+
			"FROM "+table+"\n"+
			"WHERE "+evidence+" AND Timestamp >= "+seenFrom+" AND Timestamp < "+to+and(scopeSQL)+"\n"+
			"GROUP BY EnvId")
	}
	sql := "WITH\n" +
		"act AS (\n" +
		"  SELECT EnvId, UserId, Timestamp, Client, RetentionDays\n" +
		"  FROM " + table + "\n" +
		"  WHERE " + evidence + " AND Timestamp >= " + readFrom + " AND Timestamp < " + to + and(scopeSQL) + "),\n" +
		"nu_cur AS (\n" + nuCur + "),\n" +
		"nu_prev AS (\n" + nuPrev + "),\n" +
		"nd_cur AS (\n" + newUsersDecidedFrom(table, to, scopeSQL, evidence) + "),\n" +
		"nd_prev AS (\n" + newUsersDecidedFrom(table, curFrom, scopeSQL, evidence) + ")\n" +
		strings.Join(parts, "\nUNION ALL\n")
	return b.stmt(sql), nil
}

// Parse reads the rows of Compile's statement. Every environment the scope
// names is in the answer, zero-filled, in the scope's order; environments
// found beyond those (an unrestricted scope) follow, by id. The caller closes
// rows.
func (q UserActivityQuery) Parse(rows Rows) ([]EnvUserActivity, error) {
	w := q.window()
	names := envNames(q.Scope.Envs)
	byEnv := map[string]*EnvUserActivity{}
	order := []string{}
	env := func(id string) *EnvUserActivity {
		if e, ok := byEnv[id]; ok {
			return e
		}
		e := &EnvUserActivity{EnvID: id, EnvName: names[id], Clients: []string{}, NewRule: NewUsersFirstSeen}
		if w.seriesDays > 0 {
			e.Days = make([]UserActivityDay, w.seriesDays)
			for i := range e.Days {
				e.Days[i].Day = w.seriesFrom.AddDate(0, 0, i).Format(time.DateOnly)
			}
		}
		byEnv[id] = e
		order = append(order, id)
		return e
	}
	if q.Scope.EnvRestricted {
		for _, id := range q.Scope.EnvIDs {
			env(id)
		}
	}
	restricted := len(order)
	for rows.Next() {
		var (
			kind, id, day         string
			active, nw, returning uint64
			clients               []string
			firstAt               time.Time
			retention             uint32
			flag                  uint8
		)
		if err := rows.Scan(&kind, &id, &day, &active, &nw, &returning, &clients, &firstAt, &retention, &flag); err != nil {
			return nil, err
		}
		e := env(id)
		counts := UserActivityCounts{Active: int64(active), New: int64(nw), Returning: int64(returning)}
		switch kind {
		case "cur":
			e.Current = counts
		case "prev":
			e.Previous = counts
		case "day":
			for i := range e.Days {
				if e.Days[i].Day == day {
					e.Days[i].UserActivityCounts = counts
				}
			}
		case "env":
			e.Clients = append([]string{}, clients...)
			sort.Strings(e.Clients)
			if !firstAt.IsZero() && firstAt.Unix() != 0 && (e.FirstEventAt.IsZero() || firstAt.Before(e.FirstEventAt)) {
				e.FirstEventAt = firstAt.UTC()
			}
			e.RetentionDays = max(e.RetentionDays, retention)
		case "rule":
			if flag == 1 {
				e.NewRule = NewUsersSignups
			} else {
				e.NewDecidedFrom = decidedFrom(firstAt)
			}
		case "rule_prev":
			if flag == 0 {
				e.PreviousNewDecidedFrom = decidedFrom(firstAt)
			}
		case "seen":
			n := int64(active)
			e.SeenTotal = &n
			if !firstAt.IsZero() && firstAt.Unix() != 0 && (e.FirstEventAt.IsZero() || firstAt.Before(e.FirstEventAt)) {
				e.FirstEventAt = firstAt.UTC()
			}
			e.RetentionDays = max(e.RetentionDays, retention)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	extra := append([]string{}, order[restricted:]...)
	sort.Strings(extra)
	order = append(order[:restricted], extra...)
	out := make([]EnvUserActivity, 0, len(order))
	for _, id := range order {
		e := byEnv[id]
		if q.WantSeenTotal && e.SeenTotal == nil {
			zero := int64(0)
			e.SeenTotal = &zero
		}
		out = append(out, *e)
	}
	return out, nil
}

// decidedFrom is a DecidedFrom column in UTC; zero for the epoch (sign-ups
// decide).
func decidedFrom(t time.Time) time.Time {
	if t.IsZero() || t.Unix() == 0 {
		return time.Time{}
	}
	return t.UTC()
}

// ReadUserActivity compiles q, runs it on run and parses the answer.
func ReadUserActivity(ctx context.Context, run Runner, q UserActivityQuery) ([]EnvUserActivity, error) {
	st, err := q.Compile()
	if err != nil {
		return nil, err
	}
	rows, err := run(ctx, st)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return q.Parse(rows)
}
