package obsbackend

// User activity (`forge obs users`): the shapes both backends answer in, the
// local reader over pkg/obsquery's statement, and the cloud reader over the
// control plane's ObservabilityService/GetUserActivity (by procedure name,
// with locally declared protojson shapes: camelCase fields, int64 as
// strings).

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

const procGetUserActivity = "controlplane.v1.ObservabilityService/GetUserActivity"

// UserActivityRequest counts the users of the env over the narrowed range.
type UserActivityRequest struct {
	Narrow
	// SeriesDays is the daily series' length in UTC days (0 = none).
	SeriesDays int
	// WantSeenTotal asks for the retention-wide distinct users.
	WantSeenTotal bool
	// Activity is the project's declared user-activity rules (the ones forge
	// generate compiled into its dashboards). The local reader compiles them
	// into its statement; a hosted env's answer is the control plane's.
	Activity obsquery.Activity
}

// UserCounts are the distinct users of one window.
type UserCounts struct {
	Active, New, Returning int64
}

// UserDay is one UTC day of the series.
type UserDay struct {
	Day string // YYYY-MM-DD
	UserCounts
}

// EnvUsers is one environment's users.
type EnvUsers struct {
	EnvID             string
	Current, Previous UserCounts
	Days              []UserDay
	SeenTotal         *int64
	RetentionDays     int
	FirstEventAt      time.Time
	Clients           []string
	// NewRule is how New was counted: "signups" or "first_seen" (obsquery
	// NewUsersRule); "" when the backend did not say.
	NewRule string
}

// UserActivityResult is one entry per environment.
type UserActivityResult struct {
	Envs       []EnvUsers
	Statements []Statement
}

func envUsers(e obsquery.EnvUserActivity) EnvUsers {
	out := EnvUsers{
		EnvID: e.EnvID, Current: UserCounts(e.Current), Previous: UserCounts(e.Previous),
		SeenTotal: e.SeenTotal, RetentionDays: int(e.RetentionDays), FirstEventAt: e.FirstEventAt,
		Clients: e.Clients, NewRule: string(e.NewRule),
	}
	for _, d := range e.Days {
		out.Days = append(out.Days, UserDay{Day: d.Day, UserCounts: UserCounts(d.UserActivityCounts)})
	}
	return out
}

// UserActivity implements UserActivityReader over the local ClickStack. Its
// product_events table is the control plane's, so a stack without it answers
// "could not look".
func (l Local) UserActivity(ctx context.Context, req UserActivityRequest) (UserActivityResult, error) {
	if len(req.Filters) > 0 {
		return UserActivityResult{}, Wrong("--filter narrows telemetry attributes and does not apply to users; use --service")
	}
	if req.SeriesDays < 0 || req.SeriesDays > obsquery.MaxUserActivitySeriesDays {
		return UserActivityResult{}, Wrong("--series must be 0-%d days", obsquery.MaxUserActivitySeriesDays)
	}
	sc := obsquery.Scope{Database: obsquery.LocalDatabase, Local: true, Services: req.Services, Start: req.Range.From, End: req.Range.To}
	l.Instances.apply(&sc)
	q := obsquery.UserActivityQuery{Scope: sc, SeriesDays: uint32(req.SeriesDays), WantSeenTotal: req.WantSeenTotal, Activity: req.Activity}
	st, err := q.Compile()
	if err != nil {
		return UserActivityResult{}, compileErr(err)
	}
	res := UserActivityResult{Statements: []Statement{stmt("users", st)}}
	if req.Explain {
		return res, nil
	}
	rows, err := l.rows(ctx, st)
	if err != nil {
		return res, usersErr(err)
	}
	envs, err := q.Parse(rows)
	_ = rows.Close()
	if err != nil {
		return res, usersErr(err)
	}
	for _, e := range envs {
		res.Envs = append(res.Envs, envUsers(e))
	}
	return res, nil
}

// usersErr maps a local user read's failure onto the exit table.
func usersErr(err error) error {
	var be *Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &be):
		if strings.Contains(be.Msg, "UNKNOWN_TABLE") || strings.Contains(be.Msg, "Unknown table") {
			return Undetermined("this ClickStack has no product_events table: product events are derived by the control plane; read users on a hosted env").Cause(err)
		}
		return err
	case errors.Is(err, obsquery.ErrInvalid):
		return compileErr(err)
	}
	return Undetermined("the local ClickStack returned user activity forge cannot read").Cause(err)
}

type wireUserCounts struct {
	Active    jsonInt `json:"active"`
	New       jsonInt `json:"new"`
	Returning jsonInt `json:"returning"`
}

func (w wireUserCounts) counts() UserCounts {
	return UserCounts{Active: int64(w.Active), New: int64(w.New), Returning: int64(w.Returning)}
}

type wireEnvUserActivity struct {
	EnvID    string         `json:"envId"`
	Current  wireUserCounts `json:"current"`
	Previous wireUserCounts `json:"previous"`
	Days     []struct {
		Day string `json:"day"`
		wireUserCounts
	} `json:"days"`
	SeenTotal     *jsonInt   `json:"seenTotal"`
	RetentionDays int        `json:"retentionDays"`
	FirstEventAt  *time.Time `json:"firstEventAt"`
	Clients       []string   `json:"clients"`
}

// UserActivity implements UserActivityReader: GetUserActivity.
func (c Cloud) UserActivity(ctx context.Context, req UserActivityRequest) (UserActivityResult, error) {
	if req.Explain {
		return c.explainer().UserActivity(ctx, req)
	}
	if len(req.Filters) > 0 {
		return UserActivityResult{}, Wrong("--filter narrows telemetry attributes and does not apply to users; use --service")
	}
	in := struct {
		Scope         wireScope `json:"scope"`
		SeriesDays    int       `json:"seriesDays,omitempty"`
		WantSeenTotal bool      `json:"wantSeenTotal,omitempty"`
	}{c.scope(req.Narrow), req.SeriesDays, req.WantSeenTotal}
	var out struct {
		Envs []wireEnvUserActivity `json:"envs"`
	}
	if err := c.call(ctx, procGetUserActivity, in, &out); err != nil {
		return UserActivityResult{}, err
	}
	res := UserActivityResult{}
	for _, w := range out.Envs {
		e := EnvUsers{EnvID: w.EnvID, Current: w.Current.counts(), Previous: w.Previous.counts(),
			RetentionDays: w.RetentionDays, Clients: w.Clients}
		if w.SeenTotal != nil {
			n := int64(*w.SeenTotal)
			e.SeenTotal = &n
		}
		if w.FirstEventAt != nil {
			e.FirstEventAt = w.FirstEventAt.UTC()
		}
		for _, d := range w.Days {
			e.Days = append(e.Days, UserDay{Day: d.Day, UserCounts: d.counts()})
		}
		res.Envs = append(res.Envs, e)
	}
	return res, nil
}
