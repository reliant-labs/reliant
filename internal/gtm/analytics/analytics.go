// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/observe"
	"google.golang.org/protobuf/types/known/timestamppb"

	obsv1 "github.com/reliant-labs/reliant/gen/controlplane/services/observability/v1"
	"github.com/reliant-labs/reliant/gen/controlplane/services/observability/v1/controlplanev1connect"
	"github.com/reliant-labs/reliant/internal/gtm"
)

// ObservabilityClient is the one ObservabilityService rpc this package
// calls. controlplanev1connect.ObservabilityServiceClient satisfies it.
type ObservabilityClient interface {
	QueryPanel(context.Context, *connect.Request[obsv1.QueryPanelRequest]) (*connect.Response[obsv1.QueryPanelResponse], error)
}

// Deps are what a Client reads through.
type Deps struct {
	// CP is the control plane's ObservabilityService (NewObservabilityClient).
	// nil is ErrAnalyticsUnavailable.
	CP ObservabilityClient
	// Token returns the analytics access token (GtmConfig.analytics_token),
	// read on every query. nil, or an empty token, is
	// gtm.ErrAnalyticsUnavailable: the read fails closed.
	Token func() string
	// Envs resolves AnalyticsQuery.Environment to environment ids
	// (NewEnvResolver over the same token). nil: a query naming an
	// environment is refused (ErrEnvFilterUnavailable), never read across
	// every environment.
	Envs EnvResolver
	// Log receives the causes the returned errors leave out. nil is
	// slog.Default().
	Log *slog.Logger
}

// client is Service over QueryPanel.
type client struct {
	cp    ObservabilityClient
	token func() string
	envs  EnvResolver
	log   *slog.Logger
}

var _ Service = (*client)(nil)

// New is the analytics client over d.
func New(d Deps) Service { return newClient(d) }

func newClient(d Deps) *client {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return &client{cp: d.CP, token: d.Token, envs: d.Envs, log: log}
}

// NewObservabilityClient is the control plane's ObservabilityService at
// baseURL (ControlPlaneConfig.url), over an instrumented HTTP client. The
// bearer token rides on every request, so baseURL must be https, or http to
// a loopback host (a local control plane); anything else is
// errInsecureControlPlaneURL.
func NewObservabilityClient(baseURL string) (ObservabilityClient, error) {
	return NewObservabilityClientWith(observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 30 * time.Second}), baseURL)
}

// NewObservabilityClientWith is NewObservabilityClient over a copy of
// httpClient that follows no redirect off https or to another host
// (checkRedirect).
func NewObservabilityClientWith(httpClient *http.Client, baseURL string) (ObservabilityClient, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if err := checkControlPlaneURL(base); err != nil {
		return nil, err
	}
	hc := *httpClient
	hc.CheckRedirect = checkRedirect
	return controlplanev1connect.NewObservabilityServiceClient(&hc, base), nil
}

// maxRedirects is as many redirects as net/http's default policy follows.
const maxRedirects = 10

// checkRedirect is the client's redirect policy: a redirect may stay on the
// original request's scheme and host only. It refuses one from https to
// anything else, and one to any other host (or port), so the bearer token
// is never sent in the clear or to a server the URL did not name.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errUnsafeRedirect
	}
	orig := via[0].URL
	if req.URL.Scheme != orig.Scheme || req.URL.Host != orig.Host {
		return errUnsafeRedirect
	}
	return nil
}

// checkControlPlaneURL refuses a base URL that is not https, except http to
// localhost or a loopback IP.
func checkControlPlaneURL(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil {
		return errInsecureControlPlaneURL
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return errInsecureControlPlaneURL
}

// Query runs q over orgID's product events.
func (c *client) Query(ctx context.Context, orgID string, q gtm.AnalyticsQuery) (gtm.AnalyticsResult, error) {
	token := ""
	if c.token != nil {
		token = strings.TrimSpace(c.token())
	}
	if c.cp == nil || token == "" {
		return gtm.AnalyticsResult{}, gtm.ErrAnalyticsUnavailable
	}
	if err := validate(orgID, q); err != nil {
		return gtm.AnalyticsResult{}, err
	}
	// An Environment narrows the read to the ids it resolves to; a lookup
	// that fails fails the read, never widening it to every environment.
	var envIDs []string
	if q.Environment != "" {
		if c.envs == nil {
			return gtm.AnalyticsResult{}, errEnvFilterUnavailable
		}
		ids, err := c.envs.Resolve(ctx, orgID, q.Environment, q.ProjectID)
		if err != nil {
			return gtm.AnalyticsResult{}, err
		}
		if len(ids) == 0 {
			return gtm.AnalyticsResult{}, errEnvLookupFailed
		}
		envIDs = ids
	}
	req := connect.NewRequest(buildRequest(orgID, q, envIDs))
	req.Header().Set("Authorization", "Bearer "+token)
	resp, err := c.cp.QueryPanel(ctx, req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return gtm.AnalyticsResult{}, ctxErr
		}
		mapped := mapCPError(err)
		c.log.WarnContext(ctx, "gtm analytics: QueryPanel failed",
			"template", string(q.Template), "code", connect.CodeOf(err).String(), "error", err)
		return gtm.AnalyticsResult{}, mapped
	}
	res, err := decode(q, resp.Msg)
	if err != nil {
		c.log.WarnContext(ctx, "gtm analytics: QueryPanel answer not understood",
			"template", string(q.Template), "error", err)
		return gtm.AnalyticsResult{}, errBadResponse
	}
	res.Environment, res.ProjectID, res.EnvironmentIDs = q.Environment, q.ProjectID, envIDs
	return res, nil
}

// buildRequest is q's QueryPanel request: scoped to orgID, q's window, the
// environments envIDs (none: every environment) and q.ProjectID when set,
// with the template's panel. q is validated. The control plane checks every
// id is a live environment of the org (and project). The funnel panel is
// narrowed by the control plane; the sql templates by their own {env_ids}
// predicate (scope in templates.go), which the server only binds.
func buildRequest(orgID string, q gtm.AnalyticsQuery, envIDs []string) *obsv1.QueryPanelRequest {
	panel := &obsv1.Panel{Id: "gtm-" + string(q.Template)}
	if q.Template == gtm.AnalyticsFunnel {
		panel.Kind = "funnel"
		panel.Funnel = &obsv1.PanelFunnel{
			Steps:  append([]string(nil), q.Events...),
			Window: funnelWindow(q.To.Sub(q.From)),
			By:     "user",
		}
	} else {
		panel.Kind = "sql"
		panel.Draw = "table"
		panel.Sql = buildSQL(q)
	}
	scope := &obsv1.QueryScope{
		OrgId: orgID,
		TimeRange: &obsv1.TimeRange{
			Start: timestamppb.New(q.From),
			End:   timestamppb.New(q.To),
		},
		EnvIds: append([]string(nil), envIDs...),
	}
	if q.ProjectID != "" {
		project := q.ProjectID
		scope.ProjectId = &project
	}
	return &obsv1.QueryPanelRequest{Scope: scope, Panel: panel}
}

// funnelWindow is how long a funnel journey may take: the whole query
// window, in whole hours rounded up, so a journey started in the window may
// finish anywhere in it. The window is at most 90 days (2160h).
func funnelWindow(d time.Duration) string {
	h := int64(math.Ceil(d.Hours()))
	if h < 1 {
		h = 1
	}
	return fmt.Sprintf("%dh", h)
}

// mapCPError is the fixed-message error for a QueryPanel failure.
func mapCPError(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeUnauthenticated, connect.CodePermissionDenied:
		return errCredentialRefused
	case connect.CodeFailedPrecondition:
		return errNotSetUp
	case connect.CodeResourceExhausted:
		return errQuotaExhausted
	case connect.CodeDeadlineExceeded:
		return errTimedOut
	case connect.CodeInvalidArgument, connect.CodeNotFound:
		return errQueryRefused
	}
	return gtm.ErrAnalyticsFailed
}

// decode reads resp as q's result. Every string a row carries from the
// control plane goes through redact.
func decode(q gtm.AnalyticsQuery, resp *obsv1.QueryPanelResponse) (gtm.AnalyticsResult, error) {
	res := gtm.AnalyticsResult{Template: q.Template, From: q.From, To: q.To, Truncated: resp.GetTruncated()}
	if q.Template == gtm.AnalyticsFunnel {
		steps := resp.GetFunnelSteps()
		if len(steps) != len(q.Events) {
			return gtm.AnalyticsResult{}, fmt.Errorf("funnel: %d steps for %d events", len(steps), len(q.Events))
		}
		for i, s := range steps {
			if s.GetEvent() != q.Events[i] {
				return gtm.AnalyticsResult{}, fmt.Errorf("funnel: step %d is %q, want %q", i+1, s.GetEvent(), q.Events[i])
			}
			res.Rows = append(res.Rows, gtm.AnalyticsRow{
				Event: q.Events[i],
				Step:  i + 1,
				Count: s.GetCount(),
				Rate:  s.GetRateFromFirst(),
			})
		}
		return res, nil
	}

	col := map[string]int{countColumn: -1, groupsColumn: -1, hiddenColumn: -1}
	for i, c := range resp.GetColumns() {
		if _, ok := col[c]; ok {
			col[c] = i
		}
	}
	if col[countColumn] < 0 || col[groupsColumn] < 0 {
		return gtm.AnalyticsResult{}, errors.New("sql: missing a count column")
	}
	kAnon := q.Template != gtm.AnalyticsEventTrend
	if kAnon && col[hiddenColumn] < 0 {
		return gtm.AnalyticsResult{}, errors.New("sql: no hidden_groups column")
	}
	total, summaries := int64(0), 0
	for _, r := range resp.GetRows() {
		missing := make(map[int]bool, len(r.GetMissingValueIndexes()))
		for _, m := range r.GetMissingValueIndexes() {
			missing[int(m)] = true
		}
		value := func(name string) int64 {
			i := col[name]
			if i < 0 || i >= len(r.GetValues()) || missing[i] {
				return 0
			}
			return int64(r.GetValues()[i])
		}
		g := r.GetGroup()
		if kAnon {
			switch g[kindColumn] {
			case kindSummary:
				summaries++
				total = value(groupsColumn)
				res.HiddenGroups = int(value(hiddenColumn))
				continue
			case kindGroup:
			default:
				return gtm.AnalyticsResult{}, fmt.Errorf("sql: row kind %q", g[kindColumn])
			}
		} else if n := value(groupsColumn); n > total {
			total = n
		}
		row := gtm.AnalyticsRow{Count: value(countColumn)}
		switch q.Template {
		case gtm.AnalyticsEventCounts:
			row.Event = redact(g[eventColumn])
		case gtm.AnalyticsEventTrend:
			row.Event = redact(g[eventColumn])
			day, err := time.Parse(time.DateOnly, g[dayColumn])
			if err != nil {
				return gtm.AnalyticsResult{}, fmt.Errorf("sql: day %q: %w", g[dayColumn], err)
			}
			row.Day = day
		case gtm.AnalyticsPropertyBreakdown:
			row.Event = q.Events[0]
			row.Value = redact(g[valueColumn])
		}
		res.Rows = append(res.Rows, row)
	}
	if kAnon && summaries != 1 {
		return gtm.AnalyticsResult{}, fmt.Errorf("sql: %d summary rows", summaries)
	}
	sortRows(q.Template, res.Rows)
	if len(res.Rows) > q.Limit {
		if q.Template == gtm.AnalyticsEventTrend {
			res.Rows = res.Rows[len(res.Rows)-q.Limit:]
		} else {
			res.Rows = res.Rows[:q.Limit]
		}
		res.Truncated = true
	}
	if total > int64(len(res.Rows)) {
		res.Truncated = true
	}
	// Redacted groups fold into one Redacted row (per day, for a trend),
	// their counts summed: listing them apart would say how many distinct
	// identifier-shaped values there are and how often each was seen, which
	// is what redaction hides. The folded count covers only the redacted
	// groups among the top-Limit rows returned, not every redacted group in
	// the window.
	res.Rows = foldRedacted(res.Rows)
	sortRows(q.Template, res.Rows)
	return res, nil
}

// sortRows puts rows in the template's order: a trend oldest day first, the
// rest heaviest first. A union's rows come back in no set order.
func sortRows(t gtm.AnalyticsTemplate, rows []gtm.AnalyticsRow) {
	if t == gtm.AnalyticsEventTrend {
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			if !a.Day.Equal(b.Day) {
				return a.Day.Before(b.Day)
			}
			return a.Event < b.Event
		})
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Event+"\x00"+a.Value < b.Event+"\x00"+b.Value
	})
}

// foldRedacted merges the rows that are the same once redacted (Event,
// Value and Day all equal), summing their counts.
func foldRedacted(rows []gtm.AnalyticsRow) []gtm.AnalyticsRow {
	type key struct {
		event, value string
		day          time.Time
	}
	out := rows[:0:0]
	at := make(map[key]int, len(rows))
	for _, r := range rows {
		k := key{r.Event, r.Value, r.Day}
		if i, ok := at[k]; ok {
			out[i].Count += r.Count
			continue
		}
		at[k] = len(out)
		out = append(out, r)
	}
	return out
}
