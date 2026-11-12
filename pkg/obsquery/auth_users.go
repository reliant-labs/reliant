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
//   - ActiveEvidence: the rows that count a user as active. Any
//     user-attributed product event: the standard auth events (a sign-in, the
//     auth middleware's daily resumed heartbeat), the automatic CRUD events,
//     the app's own. "Signed in during the range" is the meaning; every
//     event a signed-in user caused is the evidence.
//   - NewUsersSource: each user's "new" moment. An environment that reports
//     sign-ups (an auth_signed_up in the NewUsersWindowDays before the end
//     of the range) counts the users who signed up; one that does not counts
//     the users first seen in that window.
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
	// MaxSeenTotalDays bounds the retention-wide distinct count:
	// product_events' longest retention.
	MaxSeenTotalDays = 400
)

// EventAuthAccounts is the control plane's daily per-env account snapshot, a
// platform event that names no user's activity.
const EventAuthAccounts = "auth_accounts"

// ActiveEvidence is the predicate on product_events rows that count their
// UserId as active: any user-attributed event except a sign-out (which ends a
// session) and the platform's account snapshot.
const ActiveEvidence = "UserId != '' AND EventName NOT IN ('" + string(observe.AuthSignedOut) + "', '" + EventAuthAccounts + "')"

// NewUsersSource is a subquery over table with one row per new user of the
// window that ends at to (a DateTime64(9) SQL expression):
//
//	EnvId, UserId, NewAt DateTime64(9), ReportsSignups UInt8
//
// An environment with any auth_signed_up in the NewUsersWindowDays before to
// "reports sign-ups" (ReportsSignups = 1): its new users are the ones that
// signed up, NewAt their first sign-up. Otherwise every user first seen
// (ActiveEvidence) in that window is new, NewAt their first event. scope is
// an extra predicate on the rows ("" for none): the environments and
// services of the read. A caller counts the users with NewAt in its range.
func NewUsersSource(table, to, scope string) string {
	if scope != "" {
		scope = " AND " + scope
	}
	signup := "EventName = '" + string(observe.AuthSignedUp) + "'"
	return "SELECT EnvId, UserId, if(reports = 1, signed_up_at, first_at) AS NewAt, reports AS ReportsSignups\n" +
		"FROM (\n" +
		"  SELECT EnvId, UserId, first_at, signed_up_at, has_signup, max(has_signup) OVER (PARTITION BY EnvId) AS reports\n" +
		"  FROM (\n" +
		"    SELECT EnvId, UserId, min(Timestamp) AS first_at, minIf(Timestamp, " + signup + ") AS signed_up_at,\n" +
		"           toUInt8(countIf(" + signup + ") > 0) AS has_signup\n" +
		"    FROM " + table + "\n" +
		"    WHERE " + ActiveEvidence + " AND Timestamp >= " + to + " - INTERVAL " + strconv.Itoa(NewUsersWindowDays) + " DAY AND Timestamp < " + to + scope + "\n" +
		"    GROUP BY EnvId, UserId))\n" +
		"WHERE has_signup = 1 OR reports = 0"
}

// NewUsersRule says how an environment's new users were counted.
type NewUsersRule string

const (
	// NewUsersSignups: the users with an auth_signed_up in the range.
	NewUsersSignups NewUsersRule = "signups"
	// NewUsersFirstSeen: the environment reports no sign-ups, so the users
	// first seen in the range (within NewUsersWindowDays before its end).
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
	// SeenTotal is the distinct users with any ActiveEvidence row still
	// retained (at most MaxSeenTotalDays); nil unless WantSeenTotal.
	SeenTotal *int64
	// RetentionDays is the longest retention of the rows read (what SeenTotal
	// spans); 0 when none was read.
	RetentionDays uint32
	// FirstEventAt is the earliest ActiveEvidence row read: within the read
	// window, or within retention with WantSeenTotal. Zero: none.
	FirstEventAt time.Time
	// Clients are where the current window's rows came from (app.client:
	// "backend", "web", …), sorted.
	Clients []string
	// NewRule is how New was counted.
	NewRule NewUsersRule
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
// first event and retention), rule (flag: the env reports sign-ups) and seen
// (active_users: SeenTotal).
func (q UserActivityQuery) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeUserActivity); err != nil {
		return Stmt{}, err
	}
	if q.SeriesDays > MaxUserActivitySeriesDays {
		return Stmt{}, invalid("series_days must be at most %d", MaxUserActivitySeriesDays)
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
	to, curFrom, prevFrom, readFrom := b.nanoTime(w.to), b.nanoTime(w.curFrom), b.nanoTime(w.prevFrom), b.nanoTime(w.readFrom)

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
			"       CAST([] AS Array(String)), toDateTime64(0, 9), toUInt32(0), toUInt8(max(ReportsSignups)) AS flag\n"+
			"FROM nu_cur GROUP BY EnvId")
	if q.WantSeenTotal {
		seenFrom := b.nanoTime(w.to.Add(-MaxSeenTotalDays * 24 * time.Hour))
		parts = append(parts, "SELECT 'seen' AS kind, CAST(EnvId AS String) AS env_id, '' AS day,\n"+
			"       toUInt64(uniqExact(UserId)) AS active_users, toUInt64(0), toUInt64(0),\n"+
			"       CAST([] AS Array(String)), min(Timestamp) AS first_at, toUInt32(max(RetentionDays)) AS retention_days, toUInt8(0)\n"+
			"FROM "+table+"\n"+
			"WHERE "+ActiveEvidence+" AND Timestamp >= "+seenFrom+" AND Timestamp < "+to+and(scopeSQL)+"\n"+
			"GROUP BY EnvId")
	}
	sql := "WITH\n" +
		"act AS (\n" +
		"  SELECT EnvId, UserId, Timestamp, Client, RetentionDays\n" +
		"  FROM " + table + "\n" +
		"  WHERE " + ActiveEvidence + " AND Timestamp >= " + readFrom + " AND Timestamp < " + to + and(scopeSQL) + "),\n" +
		"nu_cur AS (\n" + NewUsersSource(table, to, scopeSQL) + "),\n" +
		"nu_prev AS (\n" + NewUsersSource(table, curFrom, scopeSQL) + ")\n" +
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
