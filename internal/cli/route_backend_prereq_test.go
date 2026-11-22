package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// A route that targets an OnHost workload renders an Envoy Gateway `Backend`.
// Envoy Gateway refuses to resolve one unless the Backend API is explicitly
// enabled in the controller's own config:
//
//	config.envoyGateway.extensionApis.enableBackend=true
//
// Without it the route applies CLEANLY and then fails at resolution:
//
//	ResolvedRefs=False(UnsupportedValue) Failed to process route rule 0
//	  backendRef 0: Backend is disabled in Envoy Gateway configuration.
//
// That is the failure mode worth a finding. A green apply followed by a 500 on
// the listener looks like a routing bug, an app bug, or a bad port — anything
// but one unset helm value on a chart in a different file. The whole cost is
// the hours spent not suspecting the controller's config.
//
// Why a FINDING and not a forge-rendered default: the Envoy Gateway chart here
// is PROJECT-OWNED — a forge.HelmChart the project declares, with its own
// `values`. forge rendering `enableBackend` into it would mean forge silently
// editing a chart the project owns, which is the one thing a declared chart's
// values are for. Worse, it would be invisible: the project's file would say
// one thing and the applied config another, and the next person to debug the
// chart would read the wrong source. So forge names the exact value and lets
// the owner set it where the declaration already lives.

func onHostRouteEntities(chartValues map[string]any) *KCLEntities {
	e := &KCLEntities{
		Workloads: []WorkloadEntity{{
			Name:    "daemon",
			Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{Runner: "go-run"}},
		}},
		HTTPRoutes: []HTTPRouteEntity{{
			Name: "daemon", Gateway: "public", Listener: "http", Workload: "daemon",
		}},
		HelmCharts: []HelmChartEntity{{
			Name: "envoy-gateway", OCI: "oci://docker.io/envoyproxy/gateway-helm",
			Version: "v1.9.2", Namespace: "envoy-gateway-system", CRDs: "gateway-api",
			Values: chartValues,
		}},
	}
	return e
}

// The finding fires when a route targets an OnHost workload and the declared
// Envoy Gateway chart does not enable the Backend API.
func TestRouteBackendPrereqFindings_NamesTheHelmValueWhenUnset(t *testing.T) {
	findings := routeBackendPrereqFindings(onHostRouteEntities(nil))

	if len(findings) == 0 {
		t.Fatal("a route targeting an OnHost workload renders an Envoy Gateway Backend, " +
			"which the controller refuses unless the Backend API is enabled; forge must say so")
	}
	joined := strings.Join(findings, "\n")
	// The EXACT value, verbatim: a finding that only says "enable the Backend
	// API" leaves the reader to find the key, which is the expensive half.
	if !strings.Contains(joined, "config.envoyGateway.extensionApis.enableBackend=true") {
		t.Errorf("the finding must name the exact helm value to set; got:\n%s", joined)
	}
	// And the chart to set it on — a project may declare several.
	if !strings.Contains(joined, "envoy-gateway") {
		t.Errorf("the finding must name the chart whose values need it; got:\n%s", joined)
	}
	// The route that needs it, so the reader can tell which declaration drove this.
	if !strings.Contains(joined, "daemon") {
		t.Errorf("the finding must name the route that renders a Backend; got:\n%s", joined)
	}
}

// Set, in either shape KCL can project it: no finding. A warning that keeps
// firing after it has been satisfied trains people to ignore the whole channel.
func TestRouteBackendPrereqFindings_SilentWhenEnabled(t *testing.T) {
	// Nested, as a values tree.
	nested := map[string]any{
		"config": map[string]any{
			"envoyGateway": map[string]any{
				"extensionApis": map[string]any{"enableBackend": true},
			},
		},
	}
	// Flattened, as a dotted key — the `--set` spelling, which a project may
	// equally well write into `values`.
	dotted := map[string]any{
		"config.envoyGateway.extensionApis.enableBackend": true,
	}

	for name, values := range map[string]map[string]any{"nested": nested, "dotted": dotted} {
		if got := routeBackendPrereqFindings(onHostRouteEntities(values)); len(got) != 0 {
			t.Errorf("%s: the prerequisite is satisfied; no finding expected, got:\n%s",
				name, strings.Join(got, "\n"))
		}
	}
}

// No OnHost-targeting route: nothing renders a Backend, so the Backend API is
// not a prerequisite and must not be reported.
func TestRouteBackendPrereqFindings_SilentWithoutAnOnHostRoute(t *testing.T) {
	// A route to a plain Service backend — the ordinary case.
	e := onHostRouteEntities(nil)
	e.HTTPRoutes = []HTTPRouteEntity{{
		Name: "api", Gateway: "public", Listener: "http", Service: "api",
	}}
	if got := routeBackendPrereqFindings(e); len(got) != 0 {
		t.Errorf("no route renders a Backend; no finding expected, got:\n%s", strings.Join(got, "\n"))
	}

	// A route whose workload is NOT OnHost resolves to that workload's
	// in-cluster Service, not a Backend.
	e = onHostRouteEntities(nil)
	e.Workloads = []WorkloadEntity{{
		Name: "daemon", Runtime: RuntimeEntity{Type: RuntimeCluster},
	}}
	if got := routeBackendPrereqFindings(e); len(got) != 0 {
		t.Errorf("the workload runs in-cluster, so the route resolves to its Service; "+
			"no finding expected, got:\n%s", strings.Join(got, "\n"))
	}
}

// A GRPCRoute renders the same Backend for an OnHost target, so it carries the
// same prerequisite. Pinned separately because the two route lists are walked
// separately and it is easy to wire one and forget the other.
func TestRouteBackendPrereqFindings_CoversGRPCRoutes(t *testing.T) {
	e := onHostRouteEntities(nil)
	e.HTTPRoutes = nil
	e.GRPCRoutes = []GRPCRouteEntity{{
		Name: "daemon-grpc", Gateway: "public", Listener: "grpc", Workload: "daemon",
	}}

	findings := routeBackendPrereqFindings(e)
	if len(findings) == 0 {
		t.Fatal("a GRPCRoute to an OnHost workload renders the same Backend and carries " +
			"the same prerequisite")
	}
	if !strings.Contains(strings.Join(findings, "\n"), "daemon-grpc") {
		t.Errorf("the finding must name the GRPCRoute; got:\n%s", strings.Join(findings, "\n"))
	}
}

// An env that renders a Backend but declares NO Envoy Gateway chart still needs
// the prerequisite — the controller is installed some other way (a cloud env's
// managed one, or out of band), and the value still has to be set there. The
// finding must say so rather than going silent because it found no chart to
// inspect: silence here reads as "satisfied", which is the opposite of true.
func TestRouteBackendPrereqFindings_ReportsWhenNoChartIsDeclared(t *testing.T) {
	e := onHostRouteEntities(nil)
	e.HelmCharts = nil

	findings := routeBackendPrereqFindings(e)
	if len(findings) == 0 {
		t.Fatal("the Backend API prerequisite holds however the controller was installed; " +
			"an env with no declared chart must still be told")
	}
	if !strings.Contains(strings.Join(findings, "\n"), "config.envoyGateway.extensionApis.enableBackend") {
		t.Errorf("the finding must still name the value; got:\n%s", strings.Join(findings, "\n"))
	}
}

// The WIRING, which the tests above cannot see: they call the pure function and
// so stay green with it detached from every user-visible surface. A finding
// nobody is shown is worth nothing, and "it is computed correctly" is exactly
// the shape of green that hides that. This drives the real printer.
func TestPrintPrerequisiteChecklist_ShowsTheBackendAPIPrerequisite(t *testing.T) {
	// The env declares no Secrets and no DNS — so the checklist's own early
	// return is the thing being tested. Before the wiring it printed nothing at
	// all for this env, which is precisely the silent case.
	entities := onHostRouteEntities(nil)

	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPrerequisiteChecklist(entities)
	_ = w.Close()
	os.Stdout = original

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.Contains(out, enableBackendHelmValue) {
		t.Errorf("the deploy's prerequisite checklist must name the Backend API value "+
			"for an env whose route targets a host process; got:\n%s", out)
	}
}

// And it must stay quiet for an env that needs nothing — the checklist's
// no-op-by-default contract. A reminder printed on every unrelated deploy is
// how the whole channel stops being read.
func TestPrintPrerequisiteChecklist_SilentForAnEnvWithNoPrereqs(t *testing.T) {
	entities := onHostRouteEntities(nil)
	entities.HTTPRoutes = nil
	entities.GRPCRoutes = nil

	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPrerequisiteChecklist(entities)
	_ = w.Close()
	os.Stdout = original

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	if out := strings.TrimSpace(buf.String()); out != "" {
		t.Errorf("an env with no prerequisites must print nothing; got:\n%s", out)
	}
}
