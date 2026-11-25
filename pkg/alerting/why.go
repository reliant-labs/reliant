// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"encoding/json"
	"fmt"
	"time"
)

// Detector names, the Why.Detector values.
const (
	DetectorIssueSpike       = "issue_spike"
	DetectorReleaseRegressed = "release_regressed"
	DetectorServiceAnomaly   = "service_anomaly"
	// DetectorUptimeCheck is a check that failed N probes in a row;
	// DetectorCertificateExpiring a certificate close to its expiry. Both read
	// the uptime prober's metrics (uptime.check.up, tls.cert.expiry_seconds).
	DetectorUptimeCheck         = "uptime_check"
	DetectorCertificateExpiring = "certificate_expiring"
)

// Why is the shared "why it fired" payload: what a detector observed, against
// what, over which window, what changed around it, and which commands lead
// from the alert to the evidence. Every detector fills the same shape, so a
// receiver (the in-app inbox, the Autopilot trigger, a Slack template) reads
// one document whatever fired.
//
// It rides with the alert instance and its notification as the rule row's
// WhyColumn, NOT as labels. A label is part of an alert's identity: a number
// that moves between evaluations (the observed count) would make each
// evaluation a new alert and notify again, and an annotation template can
// quote only labels and the value. The payload is refreshed on every
// evaluation, stored beside the instance and its history, and posted as the
// WhyAnnotation annotation.
type Why struct {
	// Detector is one of the Detector* names.
	Detector string `json:"detector"`
	// Signal is what was measured: error_rate, p95, throughput, issue_events,
	// release_health.
	Signal string `json:"signal"`
	// DedupeKey names what the alert is about, so a receiver that gets several
	// alerts for one cause (a bad deploy raises a regression, a spike and an
	// error-rate alert) can start one piece of work, not one per alert. Built
	// by DedupeKey.
	DedupeKey string `json:"dedupe_key,omitempty"`

	// Observed is the measured value in Unit; Baseline is what it is judged
	// against; Spread is the baseline's dispersion, when the method has one.
	// Nil means the detector does not have the figure, never zero.
	Observed *float64 `json:"observed,omitempty"`
	Baseline *float64 `json:"baseline,omitempty"`
	Spread   *float64 `json:"spread,omitempty"`
	// Unit names Observed, Baseline and Spread: "fraction", "ratio", "ms",
	// "events/hour".
	Unit string `json:"unit,omitempty"`
	// Method says how Baseline was found, in a line a person can check.
	Method string `json:"method,omitempty"`
	// Thresholds are the detector's own limits that Observed crossed, by name
	// ("factor": 5, "min_events": 20).
	Thresholds map[string]float64 `json:"thresholds,omitempty"`

	// Window is the span Observed covers ("15m", "1h-2h"); BaselineWindow the
	// span Baseline covers. Since is when the condition began.
	Window         string    `json:"window,omitempty"`
	BaselineWindow string    `json:"baseline_window,omitempty"`
	Since          time.Time `json:"since,omitzero"`
	// HistoryDays is how much history the baseline had to work with.
	HistoryDays int `json:"history_days,omitempty"`

	Context WhyContext `json:"context"`

	// Next are commands that lead from the alert to the evidence, runnable as
	// written ("forge obs check prod").
	Next []string `json:"next,omitempty"`
}

// WhyContext is what was going on around the alert.
type WhyContext struct {
	// Findings are the individual findings a detector judged: one per
	// service, issue or threshold that moved.
	Findings []WhyFinding `json:"findings,omitempty"`
	// Releases are the releases promoted in the window, with their commit range.
	Releases []WhyRelease `json:"releases_in_window,omitempty"`
	// Activity is the other changes in the window (a config change, a deploy).
	Activity []WhyActivity `json:"activity_in_window,omitempty"`
	// TopIssues are the issues most active in the window.
	TopIssues []WhyIssue `json:"top_issues,omitempty"`
	// ShiftedAttributes name attributes whose distribution shifted
	// ("build=3f9c: 92% vs 4% baseline").
	ShiftedAttributes []string `json:"shifted_attributes,omitempty"`
}

// WhyFinding is one thing a detector found.
type WhyFinding struct {
	// Code is the detector's own name for it (a release-health finding code).
	Code    string   `json:"code"`
	Service string   `json:"service,omitempty"`
	Ref     string   `json:"ref,omitempty"`
	Value   float64  `json:"value"`
	Limit   float64  `json:"limit"`
	Before  *float64 `json:"before,omitempty"`
	After   *float64 `json:"after,omitempty"`
	// Policy says whether the finding counted ("fail") or only warned.
	Policy  string `json:"policy,omitempty"`
	Message string `json:"message,omitempty"`
}

// WhyRelease is a promotion in the window.
type WhyRelease struct {
	Version     string    `json:"version"`
	Previous    string    `json:"previous,omitempty"`
	PromotionID string    `json:"promotion_id,omitempty"`
	PromotedAt  time.Time `json:"promoted_at"`
	// Commit and PreviousCommit bound the change: PreviousCommit..Commit.
	Commit         string `json:"commit,omitempty"`
	PreviousCommit string `json:"previous_commit,omitempty"`
}

// WhyActivity is one change in the window.
type WhyActivity struct {
	At     time.Time `json:"at"`
	Actor  string    `json:"actor,omitempty"`
	Verb   string    `json:"verb"`
	Target string    `json:"target,omitempty"`
}

// WhyIssue is an issue active in the window.
type WhyIssue struct {
	ID      string `json:"id"`
	Service string `json:"service,omitempty"`
	Title   string `json:"title,omitempty"`
	// Events is the count over the alert's window.
	Events      int64     `json:"events"`
	FirstSeenAt time.Time `json:"first_seen_at,omitzero"`
	LastSeenAt  time.Time `json:"last_seen_at,omitzero"`
}

// DedupeKey is the key alerts about one cause share: the detector, what it is
// about (a service, an env, an issue) and the release in play ("" when none).
func DedupeKey(detector, subject, release string) string {
	if release == "" {
		release = "-"
	}
	return detector + "/" + subject + "/" + release
}

// F is a pointer to v, for Why's optional figures.
func F(v float64) *float64 { return &v }

// JSON is the payload as an instance carries it: compact, and bounded by
// MaxWhyBytes. The lists with a natural order of importance are cut from the
// tail until it fits; a payload that cannot be made to fit is an error.
func (w Why) JSON() (json.RawMessage, error) {
	for {
		b, err := json.Marshal(w)
		if err != nil {
			return nil, fmt.Errorf("alerting: encoding the why payload: %w", err)
		}
		if len(b) <= MaxWhyBytes {
			return b, nil
		}
		c := &w.Context
		switch {
		case len(c.Activity) > 0:
			c.Activity = c.Activity[:len(c.Activity)-1]
		case len(c.TopIssues) > 0:
			c.TopIssues = c.TopIssues[:len(c.TopIssues)-1]
		case len(c.Findings) > 1:
			c.Findings = c.Findings[:len(c.Findings)-1]
		case len(c.ShiftedAttributes) > 0:
			c.ShiftedAttributes = c.ShiftedAttributes[:len(c.ShiftedAttributes)-1]
		default:
			return nil, fmt.Errorf("alerting: the why payload is %d bytes, the limit is %d", len(b), MaxWhyBytes)
		}
	}
}

// ParseWhy reads a payload back. Unknown fields are ignored: a newer detector
// may write more than this reader knows.
func ParseWhy(raw json.RawMessage) (Why, error) {
	var w Why
	if len(raw) == 0 {
		return w, nil
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return Why{}, fmt.Errorf("alerting: decoding the why payload: %w", err)
	}
	return w, nil
}
