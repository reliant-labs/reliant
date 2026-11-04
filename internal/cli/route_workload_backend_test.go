package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// ADR-0003 F2 — the Go side of "a route targets a workload wherever it
// runs", plus the typed per-route traffic policy.
//
// These tests pin the JSON CONTRACT between the KCL render and forge's Go
// consumers. The KCL fixtures (kcl/tests/positive_route_*.k) pin what the
// manifests look like; this pins that forge can read the projection back.
// Both halves are needed: a renamed JSON key passes every KCL assertion
// and silently decodes to a zero value here.

// A route's backend is declared as EITHER a service name or a workload
// name, and the projection carries whichever was written. An inferred
// port is ABSENT rather than pre-resolved — output.manifests is the one
// authority on the resolved backend, and a second copy here could
// disagree with it.
func TestHTTPRouteEntity_WorkloadBackendRoundTrip(t *testing.T) {
	t.Parallel()

	// A workload backend with an inferred port: no `port`, no `service`.
	const workloadBacked = `{
		"name": "registry-realm",
		"gateway": "dev-public",
		"listener": "http",
		"workload": "admin-server",
		"path": "/auth/token"
	}`

	var route HTTPRouteEntity
	if err := json.Unmarshal([]byte(workloadBacked), &route); err != nil {
		t.Fatalf("unmarshal workload-backed route: %v", err)
	}
	if route.Workload != "admin-server" {
		t.Errorf("Workload = %q, want %q", route.Workload, "admin-server")
	}
	if route.Service != "" {
		t.Errorf("Service = %q, want empty — the route named a workload", route.Service)
	}
	if route.Port != 0 {
		t.Errorf("Port = %d, want 0 — an inferred port is not pre-resolved into the projection", route.Port)
	}

	// Re-marshalling must not invent the fields that were absent, or a
	// consumer writing the projection back out would turn "inferred"
	// into "declared as zero".
	out, err := json.Marshal(route)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(out); strings.Contains(got, `"service"`) || strings.Contains(got, `"port"`) {
		t.Errorf("round-trip emitted an absent field: %s", got)
	}

	// A service backend keeps its explicit port: a Service name says
	// nothing about which port to reach, so the schema requires it.
	const serviceBacked = `{
		"name": "api",
		"gateway": "public",
		"listener": "http",
		"service": "api-svc",
		"port": 8080
	}`
	var svcRoute HTTPRouteEntity
	if err := json.Unmarshal([]byte(serviceBacked), &svcRoute); err != nil {
		t.Fatalf("unmarshal service-backed route: %v", err)
	}
	if svcRoute.Service != "api-svc" || svcRoute.Port != 8080 {
		t.Errorf("service backend = (%q, %d), want (%q, %d)",
			svcRoute.Service, svcRoute.Port, "api-svc", 8080)
	}
	if svcRoute.Workload != "" {
		t.Errorf("Workload = %q, want empty — the route named a service", svcRoute.Workload)
	}
}

// The typed traffic policy decodes field for field. The values are
// ADR-0003 S1 §5's — the configuration that took replica-loss pull
// errors from 13.1% to 0.37%.
func TestRouteTrafficEntity_RoundTrip(t *testing.T) {
	t.Parallel()

	const withTraffic = `{
		"name": "registry",
		"gateway": "public",
		"listener": "http",
		"workload": "registry",
		"traffic": {
			"retries": {
				"on": ["5xx", "reset", "connect-failure", "refused-stream"],
				"attempts": 3,
				"per_try_timeout": "5s"
			},
			"timeout": "1h",
			"health_check": {
				"path": "/v2/",
				"expected_statuses": [401],
				"interval": "1s",
				"timeout": "1s",
				"unhealthy_threshold": 3,
				"healthy_threshold": 1
			},
			"outlier_detection": {
				"consecutive_5xx": 3,
				"consecutive_gateway_errors": 3,
				"interval": "1s",
				"base_ejection_time": "10s",
				"max_ejection_percent": 67
			}
		}
	}`

	var route HTTPRouteEntity
	if err := json.Unmarshal([]byte(withTraffic), &route); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if route.Traffic == nil {
		t.Fatal("Traffic is nil — the typed policy did not decode")
	}

	if route.Traffic.Timeout != "1h" {
		t.Errorf("Timeout = %q, want %q (a multi-GB blob upload runs far past Envoy's 15s default)",
			route.Traffic.Timeout, "1h")
	}

	r := route.Traffic.Retries
	if r == nil {
		t.Fatal("Retries is nil")
	}
	if r.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", r.Attempts)
	}
	if r.PerTryTimeout != "5s" {
		t.Errorf("PerTryTimeout = %q, want %q", r.PerTryTimeout, "5s")
	}
	wantTriggers := []string{"5xx", "reset", "connect-failure", "refused-stream"}
	if len(r.On) != len(wantTriggers) {
		t.Fatalf("On = %v, want %v", r.On, wantTriggers)
	}
	for i, want := range wantTriggers {
		if r.On[i] != want {
			t.Errorf("On[%d] = %q, want %q", i, r.On[i], want)
		}
	}

	// The field that matters: a token-authed registry answers /v2/ with
	// 401 when it is HEALTHY, so a default 2xx check would mark every
	// replica down and the route would serve nothing.
	hc := route.Traffic.HealthCheck
	if hc == nil {
		t.Fatal("HealthCheck is nil")
	}
	if hc.Path != "/v2/" {
		t.Errorf("health check Path = %q, want %q", hc.Path, "/v2/")
	}
	if len(hc.ExpectedStatuses) != 1 || hc.ExpectedStatuses[0] != 401 {
		t.Errorf("ExpectedStatuses = %v, want [401]", hc.ExpectedStatuses)
	}
	if hc.UnhealthyThreshold != 3 || hc.HealthyThreshold != 1 {
		t.Errorf("thresholds = (%d, %d), want (3, 1)", hc.UnhealthyThreshold, hc.HealthyThreshold)
	}

	od := route.Traffic.OutlierDetection
	if od == nil {
		t.Fatal("OutlierDetection is nil")
	}
	if od.Consecutive5xx != 3 || od.ConsecutiveGatewayErrors != 3 {
		t.Errorf("consecutive = (%d, %d), want (3, 3)", od.Consecutive5xx, od.ConsecutiveGatewayErrors)
	}
	if od.BaseEjectionTime != "10s" {
		t.Errorf("BaseEjectionTime = %q, want %q", od.BaseEjectionTime, "10s")
	}
	// 67 keeps one of three replicas serving rather than emptying the pool.
	if od.MaxEjectionPercent != 67 {
		t.Errorf("MaxEjectionPercent = %d, want 67", od.MaxEjectionPercent)
	}
}

// A route with no traffic decodes to a nil Traffic, not to a zero-valued
// one. The distinction is load-bearing: an empty policy would read as
// protection the route does not have.
func TestRouteTrafficEntity_AbsentIsNil(t *testing.T) {
	t.Parallel()

	const noTraffic = `{"name":"plain","gateway":"public","listener":"http","service":"x","port":80}`
	var route HTTPRouteEntity
	if err := json.Unmarshal([]byte(noTraffic), &route); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if route.Traffic != nil {
		t.Errorf("Traffic = %+v, want nil for a route that declares none", route.Traffic)
	}

	out, err := json.Marshal(route)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), `"traffic"`) {
		t.Errorf("marshalled an absent traffic block: %s", out)
	}
}

// A GRPCRoute carries the same backend selection and the same typed
// policy — a route does not behave differently because it carries gRPC.
func TestGRPCRouteEntity_WorkloadAndTraffic(t *testing.T) {
	t.Parallel()

	const grpc = `{
		"name": "daemon",
		"gateway": "public",
		"listener": "grpc",
		"workload": "daemon-gateway",
		"traffic": {"retries": {"on": ["connect-failure"], "attempts": 2}}
	}`

	var route GRPCRouteEntity
	if err := json.Unmarshal([]byte(grpc), &route); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if route.Workload != "daemon-gateway" {
		t.Errorf("Workload = %q, want %q", route.Workload, "daemon-gateway")
	}
	if route.Traffic == nil || route.Traffic.Retries == nil {
		t.Fatal("traffic retries did not decode on a GRPCRoute")
	}
	if route.Traffic.Retries.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", route.Traffic.Retries.Attempts)
	}
	// An unset per-try timeout stays empty rather than becoming "0s".
	if route.Traffic.Retries.PerTryTimeout != "" {
		t.Errorf("PerTryTimeout = %q, want empty when unset", route.Traffic.Retries.PerTryTimeout)
	}
}

// raw_policy is DELETED, not deprecated (pre-1.0). It was documented as
// "emitted verbatim alongside the route" and no renderer ever emitted it
// — a field that silently does nothing reads as configuration that is in
// effect. A stale project that still sets it must not decode into
// anything forge carries forward.
func TestRouteEntity_RawPolicyIsGone(t *testing.T) {
	t.Parallel()

	const stale = `{
		"name": "api",
		"gateway": "public",
		"listener": "http",
		"service": "api",
		"port": 8080,
		"raw_policy": "kind: BackendTrafficPolicy\n"
	}`

	// Decoding is lenient by design (encoding/json ignores unknown keys),
	// so the assertion that matters is that the value does not survive
	// into the projection forge re-emits.
	var route HTTPRouteEntity
	if err := json.Unmarshal([]byte(stale), &route); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(route)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "raw_policy") {
		t.Errorf("raw_policy survived the round-trip: %s", out)
	}

	// And a Gateway carries no such field either — it was dead on all
	// three ingress kinds, so all three lost it together.
	var gw GatewayEntity
	if err := json.Unmarshal([]byte(`{"name":"public","raw_policy":"x"}`), &gw); err != nil {
		t.Fatalf("unmarshal gateway: %v", err)
	}
	gwOut, err := json.Marshal(gw)
	if err != nil {
		t.Fatalf("marshal gateway: %v", err)
	}
	if strings.Contains(string(gwOut), "raw_policy") {
		t.Errorf("Gateway raw_policy survived the round-trip: %s", gwOut)
	}
}
