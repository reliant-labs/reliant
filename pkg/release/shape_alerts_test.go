package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func shapeAlert(name string) ShapeAlert {
	return ShapeAlert{
		Alert: name, Expr: "SELECT service, 1 AS value FROM t", For: "5m", Interval: "1m",
		Labels: map[string]string{"severity": "warning", "team": "payments"}, Annotations: map[string]string{"summary": "x"},
		Source: AlertSourceUser,
	}
}

// A shape that declares no alerts encodes without the fields, so every shape
// recorded before alerting existed hashes and diffs exactly as it did.
func TestShape_AlertsOmittedWhenEmpty(t *testing.T) {
	b, err := Shape{Kind: EnvLocal}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"alerts", "alert_routes"} {
		if strings.Contains(string(b), f) {
			t.Errorf("an empty %s must not be encoded: %s", f, b)
		}
	}
}

// The wire names are the contract control-plane reads, and the rule fields are
// pkg/alerting's RuleSpec json tags, so a consumer decodes straight into it.
func TestShape_AlertsRoundTrip(t *testing.T) {
	def := shapeAlert("ServiceErrorRateHigh")
	def.Source, def.DefaultID, def.KeepFiringFor = AlertSourceDefault, "error-rate", "10m"
	in := Shape{
		Kind:   EnvSelfManaged,
		Alerts: []ShapeAlert{shapeAlert("Zeta"), def, shapeAlert("Alpha")},
		AlertRoutes: []ShapeAlertRoute{
			{Match: map[string]string{"severity": "critical"}, Receiver: "pagerduty-oncall", Continue: true},
			{Receiver: AlertReceiverSlack},
		},
	}
	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{`"alerts":[`, `"alert":"Alpha"`, `"expr":"SELECT service, 1 AS value FROM t"`, `"for":"5m"`, `"interval":"1m"`, `"keep_firing_for":"10m"`, `"source":"default"`, `"default_id":"error-rate"`, `"alert_routes":[`, `"receiver":"pagerduty-oncall"`, `"continue":true`, `"match":{"severity":"critical"}`} {
		if !strings.Contains(string(b), f) {
			t.Errorf("encoded shape lacks %s: %s", f, b)
		}
	}
	var out Shape
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("a round-tripped shape is invalid: %v", err)
	}
	want := in.Canonical()
	if !reflect.DeepEqual(out.Alerts, want.Alerts) || !reflect.DeepEqual(out.AlertRoutes, want.AlertRoutes) {
		t.Errorf("round trip changed the alerts:\n got %+v %+v\nwant %+v %+v", out.Alerts, out.AlertRoutes, want.Alerts, want.AlertRoutes)
	}
	var names []string
	for _, a := range want.Alerts {
		names = append(names, a.Alert)
	}
	if !reflect.DeepEqual(names, []string{"Alpha", "ServiceErrorRateHigh", "Zeta"}) {
		t.Errorf("alerts are not sorted by name: %v", names)
	}
	if want.AlertRoutes[0].Receiver != "pagerduty-oncall" {
		t.Errorf("routes were reordered; first match wins, so their order is their meaning: %+v", want.AlertRoutes)
	}
}

// Two renders of the same declaration hash alike whatever order the rules were written in.
func TestShape_AlertsEncodeDeterministically(t *testing.T) {
	a := Shape{Kind: EnvLocal, Alerts: []ShapeAlert{shapeAlert("B"), shapeAlert("A")}}
	b := Shape{Kind: EnvLocal, Alerts: []ShapeAlert{shapeAlert("A"), shapeAlert("B")}}
	ea, _ := a.Encode()
	eb, _ := b.Encode()
	if string(ea) != string(eb) {
		t.Errorf("rule order changed the encoding:\n%s\n%s", ea, eb)
	}
}

func TestShape_AlertsValidate(t *testing.T) {
	good := func() Shape {
		return Shape{Kind: EnvLocal, Alerts: []ShapeAlert{shapeAlert("A")}, AlertRoutes: []ShapeAlertRoute{{Receiver: "slack"}}}
	}
	if err := good().Validate(); err != nil {
		t.Fatalf("the good shape is invalid: %v", err)
	}
	cases := map[string]func(*Shape){
		"no name":          func(s *Shape) { s.Alerts[0].Alert = " " },
		"duplicate name":   func(s *Shape) { s.Alerts = append(s.Alerts, shapeAlert("A")) },
		"no expr":          func(s *Shape) { s.Alerts[0].Expr = "" },
		"second statement": func(s *Shape) { s.Alerts[0].Expr = "SELECT 1; DROP TABLE t" },
		"bad source":       func(s *Shape) { s.Alerts[0].Source = "builtin" },
		"bad for":          func(s *Shape) { s.Alerts[0].For = "soon" },
		"interval floor":   func(s *Shape) { s.Alerts[0].Interval = "30s" },
		"no severity":      func(s *Shape) { delete(s.Alerts[0].Labels, "severity") },
		"bad severity":     func(s *Shape) { s.Alerts[0].Labels["severity"] = "page" },
		"reserved label":   func(s *Shape) { s.Alerts[0].Labels["reliant_org_id"] = "x" },
		"alertname label":  func(s *Shape) { s.Alerts[0].Labels["alertname"] = "x" },
		"url receiver":     func(s *Shape) { s.AlertRoutes[0].Receiver = "https://hooks.slack.com/x" },
		"no receiver":      func(s *Shape) { s.AlertRoutes[0].Receiver = "" },
		"no default route": func(s *Shape) { s.AlertRoutes[0].Match = map[string]string{"severity": "critical"} },
		"routes no alerts": func(s *Shape) { s.Alerts = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := good()
			s.Alerts = []ShapeAlert{shapeAlert("A")}
			mutate(&s)
			if err := s.Validate(); err == nil || !errors.Is(err, ErrInvalid) {
				t.Errorf("want ErrInvalid, got %v", err)
			}
		})
	}
}

// A shape cannot carry a value: no field of an alert or a route may hold a credential.
func TestShape_AlertRouteHasNoSecretField(t *testing.T) {
	typ := reflect.TypeOf(ShapeAlertRoute{})
	for i := 0; i < typ.NumField(); i++ {
		switch n := strings.ToLower(typ.Field(i).Name); n {
		case "url", "token", "key", "secret", "password", "webhook":
			t.Errorf("ShapeAlertRoute has a %q field: a route names a receiver, never carries its secret", n)
		}
	}
}
