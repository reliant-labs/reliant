package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// The summary must say where the HyperDX UI and the OTLP ports are: their host
// ports are declared in KCL but a bare ":8080" does not say which is the UI.
func TestUpSummaryListsWhatComposeWorkloadsPublish(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{
		{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHost}},
		{Name: "clickstack", Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{Service: "clickstack"}}},
	}}
	asked := ""
	fake := func(_ context.Context, _ string, spec *deploytarget.ComposeSpec, service string) ([]composePublisher, error) {
		asked = service
		return []composePublisher{
			{URL: "127.0.0.1", TargetPort: 4317, PublishedPort: 44317, Protocol: "tcp"},
			{URL: "127.0.0.1", TargetPort: 8080, PublishedPort: 18180, Protocol: "tcp"},
			{URL: "::", TargetPort: 8080, PublishedPort: 18180, Protocol: "tcp"}, // the IPv6 twin
		}, nil
	}

	rows := composeRows(context.Background(), e, t.TempDir(), "dev", nil, fake)
	if asked != "clickstack" {
		t.Fatalf("asked docker about %q, want the compose service clickstack", asked)
	}
	if len(rows) != 2 {
		t.Fatalf("want one row per published port (IPv4/IPv6 twins folded), got %+v", rows)
	}
	if rows[0].Name != "OTLP/gRPC" || rows[1].URL != "http://127.0.0.1:18180" || rows[1].Name != "HyperDX UI" {
		t.Errorf("rows = %+v", rows)
	}

	var out bytes.Buffer
	renderUpSummary(&out, "dev", rows, "starting", false, nil, nil)
	for _, want := range []string{"Compose services", "HyperDX UI", "http://127.0.0.1:18180", "docker compose logs clickstack"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("summary is missing %q:\n%s", want, out.String())
		}
	}
}

// One `forge env up` converged the clickstack service three times — the infra
// pre-warm, the deploy phase, then the host phase's own pre-warm — pulling
// the image each time. Once the pre-warm has converged the infra, the deploy
// phase leaves those groups alone; cluster groups still deploy.
func TestWithoutInfraGroups_KeepsOnlyWhatThePrewarmDidNotConverge(t *testing.T) {
	got := withoutInfraGroups([]deploytarget.ServiceGroup{
		{ProviderID: "host-infra"}, {ProviderID: "compose"}, {ProviderID: "k8s-cluster"}, {ProviderID: "hosted"},
	})
	if len(got) != 2 || got[0].ProviderID != "k8s-cluster" || got[1].ProviderID != "hosted" {
		t.Fatalf("withoutInfraGroups = %+v, want only the k8s-cluster and hosted groups", got)
	}
}
