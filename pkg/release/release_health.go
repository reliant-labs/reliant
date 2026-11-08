package release

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"time"
)

// Release-health finding codes: what `forge obs check` and the control plane's
// GetReleaseHealth can report about a promotion, comparing the window after it
// with the window before it.
const (
	// HealthErrorRate: the error rate rose by more than MaxErrorRateIncrease.
	HealthErrorRate = "error_rate"
	// HealthLatency: p95 latency grew by more than MaxP95Ratio.
	HealthLatency = "latency"
	// HealthNewIssue: an issue first seen in this release, with at least
	// NewIssueMinEvents events.
	HealthNewIssue = "new_issue"
	// HealthRegressedIssue: a resolved issue regressed in the window.
	HealthRegressedIssue = "regressed_issue"
	// HealthAlertFiring: an alert opened after the promotion is still firing.
	HealthAlertFiring = "alert_firing"
	// HealthWorkloadRestarts: restarts or OOM kills after the promotion.
	HealthWorkloadRestarts = "workload_restarts"
)

// HealthFindings is every finding code, in a stable order.
var HealthFindings = []string{HealthErrorRate, HealthLatency, HealthNewIssue, HealthRegressedIssue, HealthAlertFiring, HealthWorkloadRestarts}

// Release-health window bounds.
const (
	MinHealthWindow = time.Minute
	MaxHealthWindow = 6 * time.Hour
)

// ShapeReleaseHealth is the env's release-health policy (forge.ReleaseHealth):
// the thresholds a promotion is judged by, and which findings fail it rather
// than warn. The control plane's GetReleaseHealth reads it from the env's
// recorded shape, so the console, the CLI and alerting judge a release by the
// one declaration.
//
// Shape.ReleaseHealth is nil when every value is the default
// ([DefaultReleaseHealth]), so every shape recorded before the field encodes
// and hashes as it did. A reader treats nil as the defaults.
type ShapeReleaseHealth struct {
	// WindowSeconds is the length of each of the two windows compared: before
	// the promotion and after it.
	WindowSeconds int `json:"window_seconds"`
	// MinRequests is the after-window traffic below which a service is not
	// judged (the verdict is inconclusive, not a pass).
	MinRequests int `json:"min_requests"`
	// MaxErrorRateIncrease is the absolute error-rate rise, as a fraction
	// (0.01 = one percentage point), above which error_rate fires.
	MaxErrorRateIncrease float64 `json:"max_error_rate_increase"`
	// MaxP95Ratio is the after/before p95 ratio above which latency fires.
	MaxP95Ratio float64 `json:"max_p95_ratio"`
	// NewIssueMinEvents is the events a new issue needs before it counts.
	NewIssueMinEvents int `json:"new_issue_min_events"`
	// FailOn are the finding codes that fail a release; the rest warn.
	// Sorted, no duplicates.
	FailOn []string `json:"fail_on"`
}

// DefaultReleaseHealth is the policy of an env that declares none. Latency and
// restarts warn: both are noisy on a fresh rollout.
//
// It must agree with the defaults of forge.ReleaseHealth in kcl/schema.k;
// TestReleaseHealthDefaultsAgreeWithKCL fails if they drift.
func DefaultReleaseHealth() ShapeReleaseHealth {
	return ShapeReleaseHealth{
		WindowSeconds:        int((15 * time.Minute).Seconds()),
		MinRequests:          50,
		MaxErrorRateIncrease: 0.01,
		MaxP95Ratio:          1.5,
		NewIssueMinEvents:    3,
		FailOn:               []string{HealthAlertFiring, HealthErrorRate, HealthNewIssue, HealthRegressedIssue},
	}
}

// Window is WindowSeconds as a duration.
func (h ShapeReleaseHealth) Window() time.Duration {
	return time.Duration(h.WindowSeconds) * time.Second
}

// Fails reports whether finding code fails a release under h.
func (h ShapeReleaseHealth) Fails(code string) bool { return slices.Contains(h.FailOn, code) }

// IsDefault reports whether h is the default policy, which a shape records as
// absent.
func (h *ShapeReleaseHealth) IsDefault() bool {
	return h == nil || reflect.DeepEqual(h.canonical(), DefaultReleaseHealth())
}

// canonical returns a copy with FailOn sorted, deduplicated and never nil.
func (h ShapeReleaseHealth) canonical() ShapeReleaseHealth {
	out := h
	out.FailOn = append([]string{}, h.FailOn...)
	sort.Strings(out.FailOn)
	out.FailOn = slices.Compact(out.FailOn)
	return out
}

func (h ShapeReleaseHealth) validate() error {
	if w := h.Window(); w < MinHealthWindow || w > MaxHealthWindow {
		return fmt.Errorf("%w: shape release_health window %s is outside %s to %s", ErrInvalid, w, MinHealthWindow, MaxHealthWindow)
	}
	if h.MinRequests < 1 {
		return fmt.Errorf("%w: shape release_health min_requests must be at least 1", ErrInvalid)
	}
	if h.MaxErrorRateIncrease <= 0 || h.MaxErrorRateIncrease > 1 {
		return fmt.Errorf("%w: shape release_health max_error_rate_increase %v must be a fraction in (0, 1]", ErrInvalid, h.MaxErrorRateIncrease)
	}
	if h.MaxP95Ratio <= 1 {
		return fmt.Errorf("%w: shape release_health max_p95_ratio %v must be above 1", ErrInvalid, h.MaxP95Ratio)
	}
	if h.NewIssueMinEvents < 1 {
		return fmt.Errorf("%w: shape release_health new_issue_min_events must be at least 1", ErrInvalid)
	}
	for _, c := range h.FailOn {
		if !slices.Contains(HealthFindings, c) {
			return fmt.Errorf("%w: shape release_health fails on %q (expected one of %v)", ErrInvalid, c, HealthFindings)
		}
	}
	return nil
}
