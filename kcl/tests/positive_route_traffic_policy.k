"""
Positive test — `traffic` on a route renders exactly ONE Envoy Gateway
BackendTrafficPolicy, with every field mapped (ADR-0003 F2).

The values here are ADR-0003 S1 §5's, the configuration that took
replica-loss pull errors from 784/6000 (13.1%) to 22/6000 (0.37%). The
registry is on the pod-start path, so that difference is a 13% pod-start
failure rate during any rollout or node loss.

Every assertion is a field name in Envoy Gateway v1.9.2's
BackendTrafficPolicy, verified against the CRD. The mapping is the whole
point of the test: a typo in a rendered field name does not fail the
render, it produces a policy the controller silently accepts with that
protection missing.

The non-obvious one is `expected_statuses = [401]`. A registry with token
auth answers `/v2/` with 401 when it is HEALTHY, so a default (2xx)
health check marks every replica down and the route serves nothing.
Verified against Envoy's own counters in the F2 probe: a 401-answering
backend under this check reports health_check.success/healthy, while a
404-answering one reports health_check.failure on every attempt.
"""

import forge

_target = forge.ClusterTarget {
    cluster = "gke_acme_us-central1_prod"
    namespace = "acme-prod"
}

_gw = forge.Gateway {
    name = "public"
    host = "registry.example.com"
    listeners = [
        forge.GatewayListener {
            name = "http"
            port = 80
            protocol = "HTTP"
        }
        forge.GatewayListener {
            name = "grpc"
            port = 8443
            protocol = "H2C"
        }
    ]
}

_registry = forge.Workload {
    name = "registry"
    # Full reference: a cluster runtime must pull it, and the registry is
    # part of the image (there is no env-level registry).
    image = "docker.io/library/registry:3.1.2"
    ports = [{name = "api", port = 5000, expose = True}]
    runtime = forge.OnCluster {target = _target}
}

# The full S1 §5 policy.
_registry_route = forge.HTTPRoute {
    name = "registry"
    gateway = "public"
    listener = "http"
    workload = "registry"
    traffic = forge.RouteTraffic {
        retries = forge.RouteRetries {
            on = ["5xx", "reset", "connect-failure", "refused-stream"]
            attempts = 3
            per_try_timeout = "5s"
        }
        # A multi-GB blob upload runs far past Envoy's 15s default.
        timeout = "1h"
        health_check = forge.RouteHealthCheck {
            path = "/v2/"
            expected_statuses = [401]
            interval = "1s"
            timeout = "1s"
            unhealthy_threshold = 3
            healthy_threshold = 1
        }
        outlier_detection = forge.RouteOutlierDetection {
            consecutive_5xx = 3
            consecutive_gateway_errors = 3
            interval = "1s"
            base_ejection_time = "10s"
            max_ejection_percent = 67
        }
    }
}

# A GRPCRoute carries the SAME typed policy — a route does not behave
# differently because it carries gRPC.
_grpc_route = forge.GRPCRoute {
    name = "daemon"
    gateway = "public"
    listener = "grpc"
    service = "daemon-gateway"
    port = 9090
    traffic = forge.RouteTraffic {
        retries = forge.RouteRetries {
            on = ["connect-failure"]
            attempts = 2
        }
    }
}

# A route with NO traffic renders no policy at all.
_plain_route = forge.HTTPRoute {
    name = "plain"
    gateway = "public"
    listener = "http"
    service = "other"
    port = 8080
    path = "/plain"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [_registry]
    gateways = [_gw]
    http_routes = [_registry_route, _plain_route]
    grpc_routes = [_grpc_route]
}

_manifests = forge.render(_bundle).manifests
_policies = [m for m in _manifests if m.kind == "BackendTrafficPolicy"]
_p = [m for m in _policies if m.metadata.name == "registry"][0]
_grpc_p = [m for m in _policies if m.metadata.name == "daemon"][0]

# ── ONE policy per route that declares traffic, and no more ────────────
# Two routes declare `traffic` (one HTTP, one gRPC); `plain` does not.
assert_policy_count = len(_policies) == 2
assert_no_policy_for_plain = len([m for m in _policies if m.metadata.name == "plain"]) == 0

assert_policy_api = _p.apiVersion == "gateway.envoyproxy.io/v1alpha1"
assert_policy_ns = _p.metadata.namespace == "acme-prod"

# ── targetRef binds the policy to its own route, by kind ──────────────
assert_target_group = _p.spec.targetRefs[0].group == "gateway.networking.k8s.io"
assert_target_kind = _p.spec.targetRefs[0].kind == "HTTPRoute"
assert_target_name = _p.spec.targetRefs[0].name == "registry"
assert_grpc_target_kind = _grpc_p.spec.targetRefs[0].kind == "GRPCRoute"

# ── retries -> spec.retry ─────────────────────────────────────────────
assert_retry_attempts = _p.spec.retry.numRetries == 3
assert_retry_triggers = _p.spec.retry.retryOn.triggers == ["5xx", "reset", "connect-failure", "refused-stream"]
assert_retry_per_try = _p.spec.retry.perRetry.timeout == "5s"

# An unset per_try_timeout emits no perRetry block at all.
assert_grpc_no_per_retry = "perRetry" not in _grpc_p.spec.retry

# ── timeout -> spec.timeout.http.requestTimeout ───────────────────────
assert_request_timeout = _p.spec.timeout.http.requestTimeout == "1h"

# ── health_check -> spec.healthCheck.active ───────────────────────────
_active = _p.spec.healthCheck.active
assert_hc_type = _active.type == "HTTP"
assert_hc_path = _active.http.path == "/v2/"
assert_hc_expected_401 = _active.http.expectedStatuses == [401]
assert_hc_interval = _active.interval == "1s"
assert_hc_timeout = _active.timeout == "1s"
assert_hc_unhealthy = _active.unhealthyThreshold == 3
assert_hc_healthy = _active.healthyThreshold == 1

# ── outlier_detection -> spec.healthCheck.passive ─────────────────────
_passive = _p.spec.healthCheck.passive
assert_od_5xx = _passive.consecutive5XxErrors == 3
assert_od_gateway = _passive.consecutiveGatewayErrors == 3
assert_od_interval = _passive.interval == "1s"
assert_od_ejection_time = _passive.baseEjectionTime == "10s"
assert_od_max_percent = _passive.maxEjectionPercent == 67

# A policy with neither health_check nor outlier_detection emits no
# healthCheck block (rather than an empty one the controller would read).
assert_grpc_no_health_check = "healthCheck" not in _grpc_p.spec

# ── The JSON projection carries the typed policy ──────────────────────
_out = forge.render(_bundle)
_json_route = [r for r in _out.http_routes if r.name == "registry"][0]
assert_json_retry_attempts = _json_route.traffic.retries.attempts == 3
assert_json_timeout = _json_route.traffic.timeout == "1h"
assert_json_expected_statuses = _json_route.traffic.health_check.expected_statuses == [401]
assert_json_od_max = _json_route.traffic.outlier_detection.max_ejection_percent == 67
