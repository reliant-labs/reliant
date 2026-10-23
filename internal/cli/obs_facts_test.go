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
			output:  `{"observability": {"enabled": true}}`,
			want:    obsbackend.Facts{Env: "e", Declared: true, Enabled: true},
			backend: "local",
		},
		{
			name:    "a control plane alone does not move telemetry off the machine",
			output:  `{"observability": {"enabled": true}, ` + cp + `}`,
			want:    obsbackend.Facts{Env: "e", Declared: true, Enabled: true, ControlPlane: "https://cp.example.com"},
			backend: "local",
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
			if got.Env != c.want.Env || got.Declared != c.want.Declared || got.Enabled != c.want.Enabled || got.ControlPlane != c.want.ControlPlane ||
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
