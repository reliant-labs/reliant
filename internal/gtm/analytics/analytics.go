// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/types/known/timestamppb"

	obsv1 "github.com/reliant-labs/reliant/gen/controlplane/services/observability/v1"
	"github.com/reliant-labs/reliant/gen/controlplane/services/observability/v1/controlplanev1connect"
	"github.com/reliant-labs/reliant/internal/gtm"
)

// Errors. The validation errors are InvalidArgument; every control-plane
// failure is, or wraps, gtm.ErrAnalyticsFailed with a fixed message. None
// carries the control plane's own error text, which is logged instead.
var (
	// ErrNoOrg is a query with no org to scope it to.
	ErrNoOrg = svcerr.InvalidArgument("no org to read product analytics for")
	// ErrInvalidName is an event or property name outside the allowed
	// characters.
	ErrInvalidName = svcerr.InvalidArgument("an event or property name is not allowed")
	// ErrIdentifierProperty is a property_breakdown over a property that
	// names an identifier (a user, session, email, ...).
	ErrIdentifierProperty = svcerr.InvalidArgument("property_breakdown cannot group by an identifier property")
	// ErrDuplicateStep is a funnel naming an event twice.
	ErrDuplicateStep = svcerr.InvalidArgument("a funnel's steps must be distinct events")
	// ErrWindowTooLong is a sql template over more than MaxSQLWindow.
	ErrWindowTooLong = svcerr.InvalidArgument("event_counts, event_trend and property_breakdown read at most 30 days")

	// ErrCredentialRefused is the control plane refusing the analytics
	// token (Unauthenticated or PermissionDenied).
	ErrCredentialRefused = fmt.Errorf("%w: the control plane refused the analytics credential", gtm.ErrAnalyticsFailed)
	// ErrNotSetUp is an org whose observability is not set up
	// (FailedPrecondition).
	ErrNotSetUp = fmt.Errorf("%w: the org's product analytics is not set up", gtm.ErrAnalyticsFailed)
	// ErrQuotaExhausted is the org's hourly query quota or a query limit
	// (ResourceExhausted).
	ErrQuotaExhausted = fmt.Errorf("%w: the org's analytics query limit was reached; try again later", gtm.ErrAnalyticsFailed)
	// ErrTimedOut is a query that ran out of time (DeadlineExceeded).
	ErrTimedOut = fmt.Errorf("%w: the query timed out", gtm.ErrAnalyticsFailed)
	// ErrQueryRefused is the control plane refusing the query itself
	// (InvalidArgument, NotFound).
	ErrQueryRefused = fmt.Errorf("%w: the control plane refused the query", gtm.ErrAnalyticsFailed)
	// ErrBadResponse is an answer this package cannot read.
	ErrBadResponse = fmt.Errorf("%w: the control plane's answer was not understood", gtm.ErrAnalyticsFailed)
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
	// Log receives the causes the returned errors leave out. nil is
	// slog.Default().
	Log *slog.Logger
}

// client is Service over QueryPanel.
type client struct {
	cp    ObservabilityClient
	token func() string
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
	return &client{cp: d.CP, token: d.Token, log: log}
}

// NewObservabilityClient is the control plane's ObservabilityService at
// baseURL (ControlPlaneConfig.url), over an instrumented HTTP client.
func NewObservabilityClient(baseURL string) ObservabilityClient {
	return NewObservabilityClientWith(observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 30 * time.Second}), baseURL)
}

// NewObservabilityClientWith is NewObservabilityClient over httpClient.
func NewObservabilityClientWith(httpClient *http.Client, baseURL string) ObservabilityClient {
	return controlplanev1connect.NewObservabilityServiceClient(httpClient, strings.TrimRight(baseURL, "/"))
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
	req := connect.NewRequest(buildRequest(orgID, q))
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
		return gtm.AnalyticsResult{}, ErrBadResponse
	}
	return res, nil
}

// buildRequest is q's QueryPanel request: scoped to orgID and q's window,
// with the template's panel. q is validated.
func buildRequest(orgID string, q gtm.AnalyticsQuery) *obsv1.QueryPanelRequest {
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
	return &obsv1.QueryPanelRequest{
		Scope: &obsv1.QueryScope{
			OrgId: orgID,
			TimeRange: &obsv1.TimeRange{
				Start: timestamppb.New(q.From),
				End:   timestamppb.New(q.To),
			},
		},
		Panel: panel,
	}
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
		return ErrCredentialRefused
	case connect.CodeFailedPrecondition:
		return ErrNotSetUp
	case connect.CodeResourceExhausted:
		return ErrQuotaExhausted
	case connect.CodeDeadlineExceeded:
		return ErrTimedOut
	case connect.CodeInvalidArgument, connect.CodeNotFound:
		return ErrQueryRefused
	}
	return gtm.ErrAnalyticsFailed
}

// decode reads resp as q's result.
func decode(q gtm.AnalyticsQuery, resp *obsv1.QueryPanelResponse) (gtm.AnalyticsResult, error) {
	res := gtm.AnalyticsResult{Template: q.Template, From: q.From, To: q.To, Truncated: resp.GetTruncated()}
	if q.Template == gtm.AnalyticsFunnel {
		steps := resp.GetFunnelSteps()
		if len(steps) != len(q.Events) {
			return gtm.AnalyticsResult{}, fmt.Errorf("funnel: %d steps for %d events", len(steps), len(q.Events))
		}
		for i, s := range steps {
			res.Rows = append(res.Rows, gtm.AnalyticsRow{
				Event: q.Events[i],
				Step:  i + 1,
				Count: s.GetCount(),
				Rate:  s.GetRateFromFirst(),
			})
		}
		return res, nil
	}

	count, groups := -1, -1
	for i, col := range resp.GetColumns() {
		switch col {
		case countColumn:
			count = i
		case groupsColumn:
			groups = i
		}
	}
	if count < 0 {
		return gtm.AnalyticsResult{}, errors.New("sql: no count column")
	}
	total := int64(0)
	for _, r := range resp.GetRows() {
		missing := make(map[int]bool, len(r.GetMissingValueIndexes()))
		for _, m := range r.GetMissingValueIndexes() {
			missing[int(m)] = true
		}
		value := func(i int) int64 {
			if i < 0 || i >= len(r.GetValues()) || missing[i] {
				return 0
			}
			return int64(r.GetValues()[i])
		}
		if n := value(groups); n > total {
			total = n
		}
		row := gtm.AnalyticsRow{Count: value(count)}
		g := r.GetGroup()
		switch q.Template {
		case gtm.AnalyticsEventCounts:
			row.Event = g[eventColumn]
		case gtm.AnalyticsEventTrend:
			row.Event = g[eventColumn]
			day, err := time.Parse(time.DateOnly, g[dayColumn])
			if err != nil {
				return gtm.AnalyticsResult{}, fmt.Errorf("sql: day %q: %w", g[dayColumn], err)
			}
			row.Day = day
		case gtm.AnalyticsPropertyBreakdown:
			row.Event = q.Events[0]
			row.Value = g[valueColumn]
		}
		res.Rows = append(res.Rows, row)
	}
	if len(res.Rows) > q.Limit {
		res.Rows = res.Rows[:q.Limit]
		res.Truncated = true
	}
	if total > int64(len(res.Rows)) {
		res.Truncated = true
	}
	return res, nil
}
