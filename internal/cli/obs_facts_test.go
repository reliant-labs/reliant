package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/obsbackend"
)

// obsFactsFromKCL reads the facts the resolver decides on out of the rendered
// declaration; this feeds the production decoder a rendered document through
// the render fixture seam and checks the facts, and then the decision.
func TestObsFactsFromRenderedDeclaration(t *testing.T) {
	cp := `"control_plane": {"type": "reliant", "endpoint": "https://cp.example.com"}`
	host := `"workloads": [{"name": "api", "kind": "service", "runtime": {"type": "host"}, "spec": {"kind": "service"}}]`
	cluster := `"workloads": [{"name": "api", "kind": "service", "runtime": {"type": "cluster"}, "spec": {"kind": "service"}}]`
	cases := []struct {
		name    string
		output  string
		want    obsbackend.Facts
		backend string
		errHas  string
	}{
		{
			name:   "no observability block",
			output: `{}`,
			want:   obsbackend.Facts{Env: "e"},
			errHas: "telemetry is off for e",
		},
		{
			name:   "observability disabled",
			output: `{"observability": {"enabled": false}}`,
			want:   obsbackend.Facts{Env: "e", Declared: true},
			errHas: "disabled",
		},
		{
			name:    "local stack",
			output:  `{"observability": {"enabled": true}, ` + host + `}`,
			want:    obsbackend.Facts{Env: "e", Declared: true, Enabled: true, HasLocal: true},
			backend: "local",
		},
		{
			name:    "a control plane alone does not move telemetry off the machine",
			output:  `{"observability": {"enabled": true}, ` + cp + `, ` + host + `}`,
			want:    obsbackend.Facts{Env: "e", Declared: true, Enabled: true, ControlPlane: "https://cp.example.com", HasLocal: true},
			backend: "local",
		},
		{
			// control-plane prod: every workload on a cluster, no destination.
			// The local ClickStack never saw its telemetry.
			name:    "a cluster-only env automatically queries its control plane",
			output:  `{"observability": {"enabled": true}, ` + cp + `, ` + cluster + `}`,
			want:    obsbackend.Facts{Env: "e", Declared: true, Enabled: true, ControlPlane: "https://cp.example.com", HasCluster: true},
			backend: "cloud",
		},
		{
			name:   "mixed local and cluster needs a destination",
			output: `{"observability":{"enabled":true}, ` + cp + `, "workloads":[{"name":"api","kind":"service","runtime":{"type":"cluster"},"spec":{"kind":"service"}},{"name":"tools","kind":"worker","runtime":{"type":"host"},"spec":{"kind":"worker"}}]}`,
			want:   obsbackend.Facts{Env: "e", Declared: true, Enabled: true, HasLocal: true, HasCluster: true, ControlPlane: "https://cp.example.com"},
			errHas: "mixes local and cluster",
		},
		{
			// control-plane dev: host services plus local k3d workloads.
			name:    "explicit local lifecycle keeps mixed host and k3d local",
			output:  `{"lifecycle":"local","observability":{"enabled":true}, ` + cp + `, "workloads":[{"name":"api","kind":"service","runtime":{"type":"host"},"spec":{"kind":"service"}},{"name":"operator","kind":"service","runtime":{"type":"cluster"},"spec":{"kind":"service"}}]}`,
			want:    obsbackend.Facts{Env: "e", Declared: true, Enabled: true, HasLocal: true, HasCluster: true, LocalLifecycle: true, ControlPlane: "https://cp.example.com"},
			backend: "local",
		},
		{
			name:   "missing runtime never guesses local",
			output: `{"observability":{"enabled":true}, ` + cp + `}`,
			want:   obsbackend.Facts{Env: "e", Declared: true, Enabled: true, ControlPlane: "https://cp.example.com"},
			errHas: "declares no runtime",
		},
		{
			name:    "destination fed by the control plane",
			output:  `{"observability": {"enabled": true, "destination": {"endpoint": "https://otlp.cp", "protocol": "http", "token_from_control_plane": true}}, ` + cp + `}`,
			want:    obsbackend.Facts{Env: "e", Declared: true, Enabled: true, ControlPlane: "https://cp.example.com", Destination: &obsbackend.Destination{Endpoint: "https://otlp.cp", TokenFromControlPlane: true}},
			backend: "cloud",
		},
		{
			name:   "third-party destination",
			output: `{"observability": {"enabled": true, "destination": {"endpoint": "https://otlp.datadoghq.com", "protocol": "http"}}, ` + cp + `}`,
			want:   obsbackend.Facts{Env: "e", Declared: true, Enabled: true, ControlPlane: "https://cp.example.com", Destination: &obsbackend.Destination{Endpoint: "https://otlp.datadoghq.com"}},
			errHas: "goes to https://otlp.datadoghq.com",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{"output": `+c.output+`}`))
			got, err := obsFactsFromKCL(context.Background(), io.Discard, "e")
			if err != nil {
				t.Fatal(err)
			}
			if got.Env != c.want.Env || got.Declared != c.want.Declared || got.Enabled != c.want.Enabled || got.ControlPlane != c.want.ControlPlane || got.HasLocal != c.want.HasLocal || got.HasCluster != c.want.HasCluster || got.LocalLifecycle != c.want.LocalLifecycle ||
				(got.Destination == nil) != (c.want.Destination == nil) || (got.Destination != nil && *got.Destination != *c.want.Destination) {
				t.Errorf("facts = %+v (destination %+v), want %+v (destination %+v)", got, got.Destination, c.want, c.want.Destination)
			}
			dec, err := obsbackend.Resolve(got, "")
			switch {
			case c.errHas != "":
				if err == nil || !strings.Contains(err.Error(), c.errHas) {
					t.Errorf("resolve error = %v, want it to contain %q", err, c.errHas)
				}
			case err != nil || dec.Backend != c.backend:
				t.Errorf("resolve = %+v, %v; want backend %s", dec, err, c.backend)
			}
		})
	}
}

func TestObsFactsRenderFailureIsExit2NamingEnvList(t *testing.T) {
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", "")
	t.Chdir(t.TempDir())
	_, err := obsFactsFromKCL(context.Background(), io.Discard, "nope")
	if exitCodeForError(err) != 2 || !strings.Contains(err.Error(), `env "nope"`) {
		t.Fatalf("err = %v", err)
	}
	if got := obsbackend.SuggestedOf(err); len(got) != 1 || got[0] != "forge env list" {
		t.Errorf("remedy = %v", got)
	}
}
