package telemetry

import (
	"encoding/json"
	"errors"
	"testing"
)

// Validate judges each declared dashboard with the validator the caller
// passes (forge passes dashspec.ParseDashboard), naming it by its field, and
// keeps a dashboard id unique in the env whether or not one is passed.
func TestValidateDashboardsUsesTheInjectedValidator(t *testing.T) {
	a := workloadTarget()
	a.Dashboards = []json.RawMessage{json.RawMessage(`{"id": "overview", "title": "A"}`)}
	b := workloadTarget()
	b.ServiceName, b.Source = "worker", "workload:worker"
	b.Dashboards = []json.RawMessage{json.RawMessage(`{"id": "overview", "title": "B"}`), json.RawMessage(`{"id": "bad"}`)}
	ts := Normalize([]Target{a, b})

	var judged []string
	refuseBad := func(name string, raw []byte) error {
		judged = append(judged, name)
		var d struct{ ID string }
		_ = json.Unmarshal(raw, &d)
		if d.ID == "bad" {
			return errors.New(name + ": panels is required")
		}
		return nil
	}
	err := Validate(ts, Options{ValidateDashboard: refuseBad})
	requireProblem(t, err,
		`workload:worker (service worker): dashboards[0].id: "overview" is also a dashboard of workload:temporal`,
		`workload:worker (service worker): dashboards[1]: dashboards[1]: panels is required`)
	if len(judged) != 3 {
		t.Errorf("the validator judged %v, want every dashboard", judged)
	}

	// Nil: no dashspec judgement, but the shape every dashboard needs (an
	// object with an id) and id uniqueness still hold.
	b.Dashboards = []json.RawMessage{json.RawMessage(`{"id": "overview"}`), json.RawMessage(`[1]`), json.RawMessage(`{"title": "no id"}`)}
	requireProblem(t, Validate(Normalize([]Target{a, b}), Options{}),
		`dashboards[0].id: "overview" is also a dashboard of workload:temporal`,
		`dashboards[1]: is not a dashboard object`,
		`dashboards[2].id: is required`)
	b.Dashboards = []json.RawMessage{json.RawMessage(`{"id": "jobs", "panels": "anything"}`)}
	if err := Validate(Normalize([]Target{a, b}), Options{}); err != nil {
		t.Errorf("with no validator, a dashboard with a unique id passes: %v", err)
	}
}

// Origin is "" (declared) or "default" (forge rendered it), and nothing else.
func TestValidateOrigin(t *testing.T) {
	for _, o := range []Origin{OriginDeclared, OriginDefault} {
		tg := workloadTarget()
		tg.Origin = o
		if err := Validate(Normalize([]Target{tg}), Options{}); err != nil {
			t.Errorf("origin %q: %v", o, err)
		}
	}
	tg := workloadTarget()
	tg.Origin = "forge"
	requireProblem(t, Validate(Normalize([]Target{tg}), Options{}), `origin: "forge" must be empty (declared) or default`)

	// The zero origin is never written, so a declared target decodes and
	// encodes exactly as before the field.
	raw, err := json.Marshal(workloadTarget())
	if err != nil {
		t.Fatal(err)
	}
	if json.Valid(raw) && containsKey(raw, "origin") {
		t.Errorf("a declared target encodes an origin: %s", raw)
	}
}

func containsKey(raw []byte, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
