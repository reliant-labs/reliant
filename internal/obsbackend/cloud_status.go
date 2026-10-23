package obsbackend

// The overview, alerts and telemetry-token deliveries over the control plane
// (`forge obs status`). The wire shapes are declared locally (protojson:
// camelCase, int64 as a string, enums by name); the field names are those of
// control-plane proto/services/observability/v1, alerting/v1 and
// telemetry_status/v1.

import (
	"context"
	"sort"
	"strings"
	"time"
)

const (
	procGetServiceOverview      = "controlplane.v1.ObservabilityService/GetServiceOverview"
	procListAlerts              = "controlplane.v1.AlertingService/ListAlerts"
	procListTelemetryDeliveries = "controlplane.v1.TelemetryStatusService/ListTelemetryDeliveries"
)

type wireServiceOverview struct {
	Service       string  `json:"service"`
	RequestCount  jsonInt `json:"requestCount"`
	RatePerSecond float64 `json:"ratePerSecond"`
	ErrorCount    jsonInt `json:"errorCount"`
	ErrorRate     float64 `json:"errorRate"`
	P50Ms         float64 `json:"p50Ms"`
	P95Ms         float64 `json:"p95Ms"`
	P99Ms         float64 `json:"p99Ms"`
	ErrorLogCount jsonInt `json:"errorLogCount"`
}

// Overview implements OverviewReader over GetServiceOverview. The control
// plane compiles the same pkg/obsquery overview the local backend runs.
func (c Cloud) Overview(ctx context.Context, req OverviewRequest) (OverviewResult, error) {
	if req.Explain {
		res, err := c.explainer().Overview(ctx, req)
		res.Statements = explained(res.Statements)
		return res, err
	}
	var out struct {
		Services []wireServiceOverview `json:"services"`
	}
	if err := c.call(ctx, procGetServiceOverview, map[string]any{"scope": c.scope(req.Narrow)}, &out); err != nil {
		return OverviewResult{}, notServed(err, "the service overview", "GetServiceOverview")
	}
	var res OverviewResult
	for _, s := range out.Services {
		res.Services = append(res.Services, ServiceOverview{
			Service: s.Service, Requests: int64(s.RequestCount), RatePerSecond: s.RatePerSecond,
			Errors: int64(s.ErrorCount), ErrorRate: s.ErrorRate,
			P50Ms: s.P50Ms, P95Ms: s.P95Ms, P99Ms: s.P99Ms, ErrorLogs: int64(s.ErrorLogCount),
		})
	}
	return res, nil
}

type wireAlertInstance struct {
	Rule        string            `json:"rule"`
	Fingerprint string            `json:"fingerprint"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	State       string            `json:"state"`
	ActiveAt    time.Time         `json:"activeAt"`
	FiringAt    time.Time         `json:"firingAt"`
	LastValue   float64           `json:"lastValue"`
}

// Alerts implements AlertReader over ListAlerts for this env: firing first,
// then pending, each oldest first (the longest-standing problem leads).
func (c Cloud) Alerts(ctx context.Context) ([]Alert, error) {
	body := map[string]any{"orgId": c.OrgID}
	if c.EnvID != "" {
		body["envId"] = c.EnvID
	}
	var out struct {
		Alerts []wireAlertInstance `json:"alerts"`
	}
	if err := c.call(ctx, procListAlerts, body, &out); err != nil {
		return nil, notServed(err, "alerts", "ListAlerts")
	}
	alerts := make([]Alert, 0, len(out.Alerts))
	for _, a := range out.Alerts {
		alerts = append(alerts, Alert{
			Rule: a.Rule, State: strings.ToLower(a.State), Fingerprint: a.Fingerprint, Labels: a.Labels,
			Summary:  a.Annotations["summary"],
			ActiveAt: unsetEpoch(a.ActiveAt), FiringAt: unsetEpoch(a.FiringAt), Value: a.LastValue,
		})
	}
	sort.SliceStable(alerts, func(i, j int) bool {
		a, b := alerts[i], alerts[j]
		if (a.State == AlertFiring) != (b.State == AlertFiring) {
			return a.State == AlertFiring
		}
		return a.since().Before(b.since())
	})
	return alerts, nil
}

// since is when the alert's condition started holding (firing: when it
// fired).
func (a Alert) since() time.Time {
	if !a.FiringAt.IsZero() {
		return a.FiringAt
	}
	return a.ActiveAt
}

type wireTelemetryDelivery struct {
	Cluster             string    `json:"cluster"`
	Namespace           string    `json:"namespace"`
	Name                string    `json:"name"`
	State               string    `json:"state"`
	ExpiresAt           time.Time `json:"expiresAt"`
	LastVerifiedAt      time.Time `json:"lastVerifiedAt"`
	LastError           string    `json:"lastError"`
	ConsecutiveFailures int       `json:"consecutiveFailures"`
}

// TelemetryDeliveries implements DeliveryReader over ListTelemetryDeliveries
// for this env.
func (c Cloud) TelemetryDeliveries(ctx context.Context) (DeliveriesResult, error) {
	body := map[string]any{"orgId": c.OrgID}
	if c.EnvID != "" {
		body["environmentId"] = c.EnvID
	}
	var out struct {
		Deliveries []wireTelemetryDelivery `json:"deliveries"`
		Enabled    bool                    `json:"enabled"`
	}
	if err := c.call(ctx, procListTelemetryDeliveries, body, &out); err != nil {
		return DeliveriesResult{}, notServed(err, "telemetry-token deliveries", "ListTelemetryDeliveries")
	}
	res := DeliveriesResult{Enabled: out.Enabled}
	for _, d := range out.Deliveries {
		res.Deliveries = append(res.Deliveries, TelemetryDelivery{
			Cluster: d.Cluster, Namespace: d.Namespace, Name: d.Name,
			State:     strings.ToLower(strings.TrimPrefix(d.State, "DELIVERY_STATE_")),
			ExpiresAt: unsetEpoch(d.ExpiresAt), LastVerifiedAt: unsetEpoch(d.LastVerifiedAt),
			LastError: d.LastError, ConsecutiveFailures: d.ConsecutiveFailures,
		})
	}
	return res, nil
}

// notServed names an Unimplemented answer for what it is: a control plane
// older than this forge. Any other error is returned as it is.
func notServed(err error, what, rpc string) error {
	if unimplemented(err) {
		return Undetermined("the control plane does not serve %s yet (%s is unimplemented)", what, rpc).Cause(err)
	}
	return err
}

// unsetEpoch turns the epoch an unset protobuf Timestamp may decode to into
// the zero time, so "absent means unknown" leaves it out of a document.
func unsetEpoch(t time.Time) time.Time {
	if t.Unix() <= 0 {
		return time.Time{}
	}
	return t.UTC()
}
