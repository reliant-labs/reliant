package obsbackend

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestResolveTable(t *testing.T) {
	cp := "https://cp.example.com"
	cases := []struct {
		name     string
		f        Facts
		override string
		backend  string
		code     int
		errHas   string
	}{
		{"observability undeclared", Facts{Env: "dev"}, "", "", 2, "telemetry is off for dev: its observability is not declared"},
		{"observability disabled", Facts{Env: "dev", Declared: true}, "", "", 2, "its observability is disabled"},
		{"no destination: local", Facts{Env: "dev", Declared: true, Enabled: true}, "", BackendLocal, 0, ""},
		{"no destination, control_plane set, nothing hosted: still local", Facts{Env: "dev", Declared: true, Enabled: true, ControlPlane: cp}, "", BackendLocal, 0, ""},
		{"token_from_control_plane: cloud", Facts{Env: "prod", Declared: true, Enabled: true, ControlPlane: cp, Destination: &Destination{TokenFromControlPlane: true}}, "", BackendCloud, 0, ""},
		{"hosted workload: cloud", Facts{Env: "prod", Declared: true, Enabled: true, ControlPlane: cp, HasHosted: true}, "", BackendCloud, 0, ""},
		{"hosted but no control_plane", Facts{Env: "prod", Declared: true, Enabled: true, HasHosted: true}, "", "", 2, "declares no control_plane"},
		{"third-party destination", Facts{Env: "prod", Declared: true, Enabled: true, ControlPlane: cp, Destination: &Destination{Endpoint: "https://otlp.datadoghq.com"}}, "", "", 2,
			"telemetry for prod goes to https://otlp.datadoghq.com; forge can query the local ClickStack or the Reliant control plane only"},
		{"override local skips the off check", Facts{Env: "prod"}, "local", BackendLocal, 0, ""},
		{"override cloud needs a control plane", Facts{Env: "dev", Declared: true, Enabled: true}, "cloud", "", 2, "declares no control_plane"},
		{"override cloud", Facts{Env: "dev", ControlPlane: cp}, "cloud", BackendCloud, 0, ""},
		{"bad override", Facts{Env: "dev"}, "remote", "", 1, `--backend "remote"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dec, err := Resolve(c.f, c.override)
			if c.errHas == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if dec.Backend != c.backend || dec.Reason == "" {
					t.Errorf("got %+v, want backend %s with a reason", dec, c.backend)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error containing %q, got %+v", c.errHas, dec)
			}
			if !strings.Contains(err.Error(), c.errHas) {
				t.Errorf("error %q lacks %q", err, c.errHas)
			}
			if code := err.(*Error).ExitCode(); code != c.code {
				t.Errorf("exit code %d, want %d", code, c.code)
			}
		})
	}
}

func TestLocalScopeIsLocalDefaultAndFiltersByEnvName(t *testing.T) {
	n := Narrow{
		Range:    Range{From: time.Unix(1000, 0), To: time.Unix(2000, 0)},
		Services: []string{"api"},
		Filters:  []Filter{{Key: "http.route", Value: "/x"}, {Key: "status", Value: "ok", Not: true}},
	}
	sc := LocalScope(n, "dev")
	if sc.Database != obsquery.LocalDatabase || !sc.Local {
		t.Fatalf("scope = %+v, want the local default database", sc)
	}
	if sc.EnvRestricted || len(sc.EnvIDs) != 0 {
		t.Errorf("local telemetry carries no reliant.env.id; the env is a name filter, got %+v", sc)
	}
	if f := sc.Filters[0]; len(sc.Filters) != 3 || f.Key != "deployment.environment.name" || f.Op != "eq" || f.Value != "dev" {
		t.Errorf("filters = %+v", sc.Filters)
	}
	if sc.Filters[2].Op != "neq" {
		t.Errorf("key!=value must be neq: %+v", sc.Filters[2])
	}
	st, err := obsquery.LogQuery{Scope: sc, Limit: 5}.Compile()
	if err != nil {
		t.Fatalf("the local scope does not compile: %v", err)
	}
	if !strings.Contains(st.SQL, "default.otel_logs") {
		t.Errorf("not the local database:\n%s", st.SQL)
	}
}

func TestParseFilter(t *testing.T) {
	for in, want := range map[string]Filter{
		"http.route=/v1/jobs": {Key: "http.route", Value: "/v1/jobs"},
		"status!=ok":          {Key: "status", Value: "ok", Not: true},
		"a=b=c":               {Key: "a", Value: "b=c"},
		"k=":                  {Key: "k"},
	} {
		got, err := ParseFilter(in)
		if err != nil || got != want {
			t.Errorf("%q: got %+v, %v want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "novalue", "=x", "!=x"} {
		if _, err := ParseFilter(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		} else if !strings.Contains(err.Error(), "key=value") {
			t.Errorf("%q: error should teach the syntax: %v", bad, err)
		}
	}
}
