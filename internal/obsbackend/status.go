package obsbackend

// The vocabulary of `forge obs status` beyond what the other verbs already
// read: the per-service overview (both backends), the env's alerts and its
// telemetry-token deliveries (cloud only; the control plane keeps that state).
// The interfaces are in contract.go; the local overview is in
// local_status.go, the cloud readers in cloud_status.go.

import (
	"encoding/json"
	"time"
)

// OverviewRequest asks for the per-service request figures of the narrowed
// scope: server spans for rate, errors and duration, logs for the error-level
// record count.
type OverviewRequest struct {
	Narrow
}

// ServiceOverview is one service's figures over the range, busiest first.
type ServiceOverview struct {
	Service       string  `json:"service"`
	Requests      int64   `json:"requests"`
	RatePerSecond float64 `json:"rate_per_second"`
	Errors        int64   `json:"errors"`
	// ErrorRate is a fraction, 0..1.
	ErrorRate float64 `json:"error_rate"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
	// ErrorLogs counts the error-level log records in the range.
	ErrorLogs int64 `json:"error_logs"`
}

// OverviewResult is every service seen in the range, busiest first.
type OverviewResult struct {
	Services   []ServiceOverview
	Statements []Statement
}

// AlertFiring is the state of an alert that fired; the other state the
// control plane reports is "pending".
const AlertFiring = "firing"

// Alert is one active instance of an alert rule: firing, or pending (its
// condition holds but its `for` has not elapsed).
type Alert struct {
	Rule        string            `json:"rule"`
	State       string            `json:"state"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	// Summary is the rule's summary annotation, when it has one.
	Summary string `json:"summary,omitempty"`
	// Why is the alert's why-fired payload (alerting.Why), as the control
	// plane carries it in the `why` annotation. Nil for an alert whose rule
	// attaches none.
	Why      json.RawMessage `json:"why,omitempty"`
	ActiveAt time.Time       `json:"active_at,omitzero"`
	FiringAt time.Time       `json:"firing_at,omitzero"`
	Value    float64         `json:"value"`
}

// Telemetry-token delivery states that leave a working token in place (or
// need none). The others are the control plane's DeliveryState names, lower
// case and unprefixed: pending, failing, expired, unbound, disabled.
const (
	DeliveryDelivered = "delivered"
	DeliveryWithdrawn = "withdrawn"
)

// TelemetryDelivery is where one declared telemetry-token Secret stands:
// whether the control plane has written a working token into the cluster
// that ships the env's telemetry.
type TelemetryDelivery struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	State     string `json:"state"`
	// ExpiresAt is when the delivered token stops working.
	ExpiresAt           time.Time `json:"expires_at,omitzero"`
	LastVerifiedAt      time.Time `json:"last_verified_at,omitzero"`
	LastError           string    `json:"last_error,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
}

// DeliveriesResult is the env's telemetry-token deliveries.
type DeliveriesResult struct {
	// Enabled says the control plane's delivery reconciler is on; when it is
	// off nothing keeps any token valid.
	Enabled    bool
	Deliveries []TelemetryDelivery
}
