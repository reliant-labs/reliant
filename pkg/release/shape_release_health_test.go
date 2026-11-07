package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A shape with no policy, or the default spelled out, encodes without the
// field, so every shape recorded before it existed hashes as it did.
func TestShape_ReleaseHealthDefaultIsAbsent(t *testing.T) {
	def := DefaultReleaseHealth()
	shuffled := def
	shuffled.FailOn = []string{HealthRegressedIssue, HealthNewIssue, HealthErrorRate, HealthAlertFiring, HealthErrorRate}
	for name, s := range map[string]Shape{
		"absent":           {Kind: EnvLocal},
		"default":          {Kind: EnvLocal, ReleaseHealth: &def},
		"default shuffled": {Kind: EnvLocal, ReleaseHealth: &shuffled},
	} {
		c := s.Canonical()
		if c.ReleaseHealth != nil {
			t.Errorf("%s: canonical shape records the default policy: %+v", name, c.ReleaseHealth)
		}
		b, err := c.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "release_health") {
			t.Errorf("%s: encoded: %s", name, b)
		}
	}
}

func TestShape_ReleaseHealthRoundTrip(t *testing.T) {
	in := Shape{Kind: EnvSelfManaged, ReleaseHealth: &ShapeReleaseHealth{
		WindowSeconds: 1800, MinRequests: 100, MaxErrorRateIncrease: 0.02, MaxP95Ratio: 2,
		NewIssueMinEvents: 5, FailOn: []string{HealthLatency, HealthErrorRate, HealthLatency},
	}}
	c := in.Canonical()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if want := []string{HealthErrorRate, HealthLatency}; !reflect.DeepEqual(c.ReleaseHealth.FailOn, want) {
		t.Errorf("fail_on not canonical: %v", c.ReleaseHealth.FailOn)
	}
	b, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"release_health"`, `"window_seconds":1800`, `"fail_on":["error_rate","latency"]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("encoded shape lacks %s: %s", want, b)
		}
	}
	var out Shape
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.ReleaseHealth, c.ReleaseHealth) {
		t.Errorf("round trip: got %+v want %+v", out.ReleaseHealth, c.ReleaseHealth)
	}
	if out.ReleaseHealth.Window() != 30*time.Minute || !out.ReleaseHealth.Fails(HealthLatency) || out.ReleaseHealth.Fails(HealthNewIssue) {
		t.Errorf("accessors: %+v", out.ReleaseHealth)
	}
}

func TestShape_ReleaseHealthValidation(t *testing.T) {
	for name, mut := range map[string]func(*ShapeReleaseHealth){
		"window too short":     func(h *ShapeReleaseHealth) { h.WindowSeconds = 30 },
		"window too long":      func(h *ShapeReleaseHealth) { h.WindowSeconds = 7 * 3600 },
		"no min requests":      func(h *ShapeReleaseHealth) { h.MinRequests = 0 },
		"error rate zero":      func(h *ShapeReleaseHealth) { h.MaxErrorRateIncrease = 0 },
		"error rate percent":   func(h *ShapeReleaseHealth) { h.MaxErrorRateIncrease = 5 },
		"p95 ratio not above1": func(h *ShapeReleaseHealth) { h.MaxP95Ratio = 1 },
		"no issue events":      func(h *ShapeReleaseHealth) { h.NewIssueMinEvents = 0 },
		"unknown finding":      func(h *ShapeReleaseHealth) { h.FailOn = []string{"cpu"} },
	} {
		h := DefaultReleaseHealth()
		mut(&h)
		err := Shape{Kind: EnvSelfManaged, ReleaseHealth: &h}.Validate()
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if err := DefaultReleaseHealth().validate(); err != nil {
		t.Errorf("the default policy is invalid: %v", err)
	}
}
