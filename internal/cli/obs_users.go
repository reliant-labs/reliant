package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const schemaObsUsers = "forge.obs.users/v1"

var obsUsersSchema = obsout.Schema{
	Name: schemaObsUsers,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.env_id", "scope.from", "scope.to", "scope.services", "scope.instance", "scope.instance.id", "scope.instance.label", "scope.instance.user",
		"query_mode",
		"envs", "envs.env_id", "envs.current", "envs.current.active", "envs.current.new", "envs.current.returning",
		"envs.previous", "envs.previous.active", "envs.previous.new", "envs.previous.returning",
		"envs.days", "envs.days.day", "envs.days.active", "envs.days.new", "envs.days.returning",
		"envs.seen_total", "envs.retention_days", "envs.first_event_at", "envs.clients", "envs.new_rule",
		"statements", "statements.purpose", "statements.sql", "statements.params.*",
		"suggested",
	},
}

type obsUserCountsDoc struct {
	Active    int64 `json:"active"`
	New       int64 `json:"new"`
	Returning int64 `json:"returning"`
}

type obsUserDayDoc struct {
	Day string `json:"day"`
	obsUserCountsDoc
}

type obsEnvUsersDoc struct {
	EnvID         string           `json:"env_id"`
	Current       obsUserCountsDoc `json:"current"`
	Previous      obsUserCountsDoc `json:"previous"`
	Days          []obsUserDayDoc  `json:"days,omitempty"`
	SeenTotal     *int64           `json:"seen_total,omitempty"`
	RetentionDays int              `json:"retention_days,omitempty"`
	FirstEventAt  time.Time        `json:"first_event_at,omitzero"`
	Clients       []string         `json:"clients"`
	NewRule       string           `json:"new_rule,omitempty"`
}

type obsUsersDoc struct {
	obsout.Head
	Scope      obsout.Scope           `json:"scope"`
	QueryMode  string                 `json:"query_mode"`
	Envs       []obsEnvUsersDoc       `json:"envs"`
	Statements []obsbackend.Statement `json:"statements,omitempty"`
	Suggested  []string               `json:"suggested,omitempty"`
}

type obsUsersOpts struct {
	obsCommon
	series    int
	seenTotal bool
}

func newObsUsersCmd() *cobra.Command {
	var o obsUsersOpts
	cmd := &cobra.Command{
		Use:   "users [env]",
		Short: "Users of the app: active, new and returning, from its product events",
		Long: `Count the env's users over the range (--since, default 7d, at most 90d) and the
range of the same length before it: active (any event a signed-in user caused,
the auth middleware's daily sign-in included), new (signed up, or first seen in
the 90 days before the end of the range when the env reports no sign-ups) and
returning (active, not new). [env] defaults to dev.

  ` + Name() + ` obs users prod
  ` + Name() + ` obs users prod --series 30 --json
  ` + Name() + ` obs users prod --seen-total

The counts are the console's Users card's: one statement (pkg/obsquery) over the
control plane's per-org product_events table. A local ClickStack has no such
table, so a local env answers "could not look". One user's sessions:
` + "`" + Name() + ` obs sessions <env> --user <id>` + "`" + `.

` + obsUsersSchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsUsers(cmd.Context(), defaultObsDeps(cmd), obsEnvArg(args, 0), o)
		},
	}
	o.register(cmd, 7*24*time.Hour, 0)
	f := cmd.Flags()
	f.IntVar(&o.series, "series", 0, fmt.Sprintf("also count each UTC day of the last N days (0-%d)", obsquery.MaxUserActivitySeriesDays))
	f.BoolVar(&o.seenTotal, "seen-total", false, "also count every user still retained (reads the whole retention)")
	return cmd
}

func (o obsUsersOpts) validate() error {
	if o.series < 0 || o.series > obsquery.MaxUserActivitySeriesDays {
		return obsbackend.Wrong("--series must be 0-%d", obsquery.MaxUserActivitySeriesDays)
	}
	if o.since > obsquery.MaxRangeUserActivity {
		return obsbackend.Wrong("--since %s is longer than the %s user activity reads", o.since, obsquery.MaxRangeUserActivity)
	}
	if len(o.filters) > 0 {
		return obsbackend.Wrong("--filter narrows telemetry attributes and does not apply to users; use --service")
	}
	return nil
}

// usersReader is the backend's user side; a backend without one cannot look.
func usersReader(b obsbackend.Backend) (obsbackend.UserActivityReader, error) {
	r, ok := b.(obsbackend.UserActivityReader)
	if !ok {
		return nil, obsbackend.Undetermined("the %s backend serves no user activity", b.Info().Backend)
	}
	return r, nil
}

func runObsUsers(ctx context.Context, d obsDeps, env string, o obsUsersOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsUsersSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsUsers, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsUsers, err) }
	if err := o.validate(); err != nil {
		return fail(err)
	}
	n, err := o.narrow(d.Now())
	if err != nil {
		return fail(err)
	}
	if n.Range.To.Sub(n.Range.From) > obsquery.MaxRangeUserActivity {
		return fail(obsbackend.Wrong("the range is longer than the %s user activity reads", obsquery.MaxRangeUserActivity))
	}
	b, _, err := o.open(ctx, d, env, "users")
	if err != nil {
		return fail(err)
	}
	r, err := usersReader(b)
	if err != nil {
		return fail(err)
	}
	res, err := r.UserActivity(ctx, obsbackend.UserActivityRequest{Narrow: n, SeriesDays: o.series, WantSeenTotal: o.seenTotal})
	if err != nil {
		return fail(err)
	}
	doc := obsUsersDoc{Scope: obsScope(b.Info(), n), QueryMode: QueryModeStructured, Envs: []obsEnvUsersDoc{}}
	if n.Explain {
		doc.Statements = res.Statements
	}
	for _, e := range res.Envs {
		doc.Envs = append(doc.Envs, envUsersDoc(e))
	}
	doc.Suggested = o.inst.carrySuggested(obsout.Suggest(obsout.Command("forge", "obs", "sessions", env)))
	doc.Stamp(schemaObsUsers, nil)
	if done, err := o.out.Emit(d.Stdout, obsUsersSchema, doc, fields); done || err != nil {
		return err
	}
	if n.Explain {
		explainText(d.Stdout, res.Statements)
		return nil
	}
	renderUsers(d.Stdout, doc)
	return nil
}

func userCountsDoc(c obsbackend.UserCounts) obsUserCountsDoc {
	return obsUserCountsDoc{Active: c.Active, New: c.New, Returning: c.Returning}
}

func envUsersDoc(e obsbackend.EnvUsers) obsEnvUsersDoc {
	out := obsEnvUsersDoc{EnvID: e.EnvID, Current: userCountsDoc(e.Current), Previous: userCountsDoc(e.Previous),
		SeenTotal: e.SeenTotal, RetentionDays: e.RetentionDays, FirstEventAt: e.FirstEventAt, Clients: e.Clients, NewRule: e.NewRule}
	if out.Clients == nil {
		out.Clients = []string{}
	}
	for _, d := range e.Days {
		out.Days = append(out.Days, obsUserDayDoc{Day: d.Day, obsUserCountsDoc: userCountsDoc(d.UserCounts)})
	}
	return out
}

func renderUsers(w io.Writer, doc obsUsersDoc) {
	if len(doc.Envs) == 0 {
		fmt.Fprintln(w, "no user activity in the range")
		return
	}
	rows := make([][]string, 0, len(doc.Envs))
	for _, e := range doc.Envs {
		rows = append(rows, []string{
			orDash(e.EnvID),
			strconv.FormatInt(e.Current.Active, 10), userDelta(e.Current.Active, e.Previous.Active),
			strconv.FormatInt(e.Current.New, 10), userDelta(e.Current.New, e.Previous.New),
			strconv.FormatInt(e.Current.Returning, 10), userDelta(e.Current.Returning, e.Previous.Returning),
			orDash(e.NewRule), orDash(strings.Join(e.Clients, ",")),
		})
	}
	obsout.Table(w, []string{"ENV", "ACTIVE", "Δ", "NEW", "Δ", "RETURNING", "Δ", "NEW BY", "FROM"}, rows, 40)
	for _, e := range doc.Envs {
		if e.SeenTotal != nil {
			fmt.Fprintf(w, "\n%s: %d users seen in the last %d days", orDash(e.EnvID), *e.SeenTotal, e.RetentionDays)
		}
		if e.FirstEventAt.IsZero() {
			fmt.Fprintf(w, "\n%s: no user activity reported yet (forge's auth middleware and managed sign-in report it; see the observability/product-events skill)", orDash(e.EnvID))
		}
	}
	for _, e := range doc.Envs {
		if len(e.Days) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s daily active, %s to %s: %s", orDash(e.EnvID), e.Days[0].Day, e.Days[len(e.Days)-1].Day, sparkline(e.Days))
	}
	fmt.Fprintln(w)
}

// userDelta is the change against the previous window, as a percentage.
func userDelta(cur, prev int64) string {
	switch {
	case prev == 0 && cur == 0:
		return "-"
	case prev == 0:
		return "new"
	}
	pct := float64(cur-prev) * 100 / float64(prev)
	return fmt.Sprintf("%+.0f%%", pct)
}

func sparkline(days []obsUserDayDoc) string {
	const bars = "▁▂▃▄▅▆▇█"
	levels := []rune(bars)
	var top int64
	for _, d := range days {
		top = max(top, d.Active)
	}
	var b strings.Builder
	for _, d := range days {
		i := 0
		if top > 0 {
			i = int(d.Active * int64(len(levels)-1) / top)
		}
		b.WriteRune(levels[i])
	}
	return b.String()
}
