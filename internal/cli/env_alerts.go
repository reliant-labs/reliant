package cli

import (
	"fmt"
	"sort"

	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/release"
)

// AlertsEntity mirrors kcl/schema.k Observability.alerts as render.k projects it.
type AlertsEntity struct {
	Rules    []AlertRuleEntity             `json:"rules"`
	Defaults map[string]AlertDefaultEntity `json:"defaults"`
	Routes   []AlertRouteEntity            `json:"routes"`
	// Receivers are the local stack's (forge.AlertReceiver); a hosted env's
	// receivers live in the control plane.
	Receivers []AlertReceiverEntity `json:"receivers"`
}

// AlertReceiverEntity is one forge.AlertReceiver.
type AlertReceiverEntity struct {
	Name             string   `json:"name"`
	Type             string   `json:"type"`
	URL              string   `json:"url,omitempty"`
	Secret           string   `json:"secret,omitempty"`
	Channel          string   `json:"channel,omitempty"`
	Severities       []string `json:"severities"`
	AlwaysAlertnames []string `json:"always_alertnames"`
}

// AlertRuleEntity is one forge.AlertRule, severity already defaulted.
type AlertRuleEntity struct {
	Alert         string            `json:"alert"`
	Expr          string            `json:"expr"`
	For           string            `json:"for"`
	Interval      string            `json:"interval"`
	KeepFiringFor string            `json:"keep_firing_for,omitempty"`
	Labels        map[string]string `json:"labels"`
	Annotations   map[string]string `json:"annotations"`
	Enabled       bool              `json:"enabled"`
}

// AlertDefaultEntity is one forge.AlertDefault.
type AlertDefaultEntity struct {
	Enabled bool               `json:"enabled"`
	Labels  map[string]string  `json:"labels"`
	For     string             `json:"for,omitempty"`
	Params  map[string]float64 `json:"params"`
}

// AlertRouteEntity is one forge.AlertRoute.
type AlertRouteEntity struct {
	Match    map[string]string `json:"match"`
	Receiver string            `json:"receiver"`
	Continue bool              `json:"continue"`
}

// alertDefaultsInput reads the project's generated alert defaults. A
// project that has not run `forge generate` since alerting landed has none:
// that is no defaults, not an error, because the env's own rules still stand.
func alertDefaultsInput(projectDir string) (obsdash.AlertInput, bool, error) {
	return obsdash.ReadAlertInput(projectDir)
}

// resolveShapeAlerts is the env's alert rules as the release shape records
// them: its own enabled rules plus the generated defaults it did not disable,
// and its routes (the in-app + Slack fan-out when it declares none).
//
// An env whose observability is off or undeclared evaluates nothing, so it
// records nothing.
func resolveShapeAlerts(projectDir, envName string, e *KCLEntities) ([]release.ShapeAlert, []release.ShapeAlertRoute, error) {
	if e == nil || e.Observability == nil || !e.Observability.Enabled {
		return nil, nil, nil
	}
	decl := e.Observability.Alerts
	if decl == nil {
		decl = &AlertsEntity{}
	}
	in, have, err := alertDefaultsInput(projectDir)
	if err != nil {
		return nil, nil, err
	}
	tuning := map[string]obsdash.AlertTuning{}
	for id, d := range decl.Defaults {
		tuning[id] = obsdash.AlertTuning{Enabled: d.Enabled, Labels: d.Labels, For: d.For, Params: d.Params}
	}
	if err := obsdash.ValidateAlertTuning(tuning); err != nil {
		return nil, nil, err
	}
	var defaults []obsdash.AlertRule
	if have {
		var disabled []string
		if e.Observability.Policy != nil {
			disabled = e.Observability.Policy.Disabled
		}
		if defaults, _, err = obsdash.ResolveAlertDefaultsFor(in, envName, tuning, disabled); err != nil {
			return nil, nil, err
		}
	}

	var out []release.ShapeAlert
	taken := map[string]bool{}
	for _, r := range decl.Rules {
		if !r.Enabled {
			continue
		}
		out = append(out, release.ShapeAlert{
			Alert: r.Alert, Expr: r.Expr, For: r.For, Interval: r.Interval, KeepFiringFor: r.KeepFiringFor,
			Labels: r.Labels, Annotations: r.Annotations, Source: release.AlertSourceUser,
		})
		taken[r.Alert] = true
	}
	for _, name := range obsdash.AlertDefaultAlertNames() {
		if taken[name] {
			return nil, nil, fmt.Errorf("observability.alerts: rule %q reuses the name of a generated default; rename it, or tune the default with Alerts.defaults", name)
		}
	}
	for _, d := range defaults {
		out = append(out, release.ShapeAlert{
			Alert: d.Alert, Expr: d.Expr, For: d.For, Interval: d.Interval,
			Labels: d.Labels, Annotations: d.Annotations, Source: release.AlertSourceDefault, DefaultID: d.ID,
		})
	}
	if len(out) == 0 {
		return nil, nil, nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alert < out[j].Alert })

	var routes []release.ShapeAlertRoute
	for _, r := range decl.Routes {
		routes = append(routes, release.ShapeAlertRoute{Match: r.Match, Receiver: r.Receiver, Continue: r.Continue})
	}
	if len(routes) == 0 {
		routes = defaultAlertRoutes()
	}
	return out, routes, nil
}

// defaultAlertRoutes is where alerts go until the author routes them: in-app
// notifications, then Slack. Never a pager.
func defaultAlertRoutes() []release.ShapeAlertRoute {
	return []release.ShapeAlertRoute{
		{Receiver: release.AlertReceiverInApp, Continue: true},
		{Receiver: release.AlertReceiverSlack},
	}
}

// alertSummary is one line per rule for `forge env shape` and
// `forge obs alerts check`.
func alertSummary(a release.ShapeAlert) string {
	return fmt.Sprintf("%s [%s, %s, for %s, every %s]", a.Alert, a.Labels["severity"], a.Source, orDash(a.For), orDash(a.Interval))
}
