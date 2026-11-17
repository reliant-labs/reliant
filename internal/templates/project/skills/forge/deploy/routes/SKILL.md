---
name: deploy/routes
description: Routes and their backends — a route targets a `service` or a `workload` (resolved per env to its Service or to the host process), port inference, and the typed per-route `traffic` policy (retries, request timeout, active health check with expected statuses, outlier detection) that replaced the dead `raw_policy`.
---

# Routes, their backends, and per-route traffic

## A route targets a WORKLOAD, wherever that workload runs

An `HTTPRoute` / `GRPCRoute` names its backend as EITHER a `service` (a
Service name, used verbatim) or a `workload` (a `forge.Workload` in the same
Bundle). Exactly one — setting both is refused, because a route has one
backend and silently picking one would be wrong half the time.

`workload` is resolved PER ENV, from the runtime that env binds it to:

```kcl
# ONE declaration, in the base ingress. No env conditional, no mode flag.
forge.HTTPRoute {
    name = "registry-realm"
    gateway = "public"
    listener = "http"
    workload = "admin-server"   # port inferred from the workload
    path = "/auth/token"
}
```

| The workload is bound to | The route renders |
|---|---|
| `forge.OnCluster` | a backendRef to its Service — identical to `service = "..."` |
| `forge.OnHost`, landing on a LOCAL k3d cluster | an Envoy Gateway `Backend` at the cluster's `host_gateway` (`host.k3d.internal`) on the workload's own `listen_ports` entry — the gateway reaches the host PROCESS |
| `OnHost` on a non-local cluster, `OnCompose`, `OnHosted`, `BuildOnly`, or a name no workload has | a render ERROR naming the route, the workload and its runtime |

So a dev env where `admin-server` is a host process and a prod env where it
is cluster-bound share one route declaration. The alternative — an
env-conditional route, or a `mode` enum — is three products with two dead
branches each.

`port` is REQUIRED with `service` (a name says nothing about ports) and when
a `workload` target exposes more than one. Otherwise it is inferred from the
single port; ambiguity is refused rather than resolved by list order.

**Why a `Backend` CRD and not an ExternalName Service.** Envoy Gateway
refuses an ExternalName Service as a backendRef outright ("...is of type
ExternalName, which is not supported as a backend; use an Envoy Gateway
Backend resource with an FQDN endpoint instead"). The `Backend` CRD requires
the Backend API enabled on the controller —
`config.envoyGateway.extensionApis.enableBackend=true` — and EG's own CRDs
applied; forge reports a route that needs them rather than leaving you with a
`ResolvedRefs=False` at apply time.

## Per-route traffic policy — `traffic`

`traffic` is typed, and renders ONE Envoy Gateway `BackendTrafficPolicy`
targeting the route. It replaced a `raw_policy` string that **nothing in
forge ever emitted**: the field existed in the schema and the JSON and was
read by no renderer, so it read as configuration that was in effect while
doing nothing. It is deleted, not deprecated — setting `raw_policy` on a
Gateway, HTTPRoute or GRPCRoute is now a hard error.

```kcl
traffic = forge.RouteTraffic {
    retries = forge.RouteRetries {
        on = ["5xx", "reset", "connect-failure", "refused-stream"]
        attempts = 3
        per_try_timeout = "5s"
    }
    timeout = "1h"                      # whole-request; blob uploads
    health_check = forge.RouteHealthCheck {
        path = "/v2/"
        expected_statuses = [401]       # 401 == healthy, with token auth
    }
    outlier_detection = forge.RouteOutlierDetection {}   # S1 defaults
}
```

Reach for it on anything on the pod-start path. Measured: losing one of
three replicas of a registry cost **13.1% of pulls** with plain round-robin
and **0.37%** with these retries plus outlier detection.

Two fields earn specific attention:

- **`expected_statuses`** is the trap. A registry with token auth answers
  `/v2/` with **401** when it is perfectly healthy, so a default (2xx) check
  marks every replica down and the route serves nothing.
- **`timeout`** is the whole-request timeout. A multi-GB blob upload runs far
  past Envoy's 15s default, which surfaces as a push that dies partway with
  no server-side error. Envoy STREAMS request bodies and forge enables no
  buffering anywhere, which is what makes those uploads possible at all.

One limitation worth knowing: Envoy Gateway cannot express a `previous_hosts`
retry-host predicate, so a retry may re-select the endpoint that just failed.
`outlier_detection` is the mitigation — an ejected endpoint cannot be
selected by a retry. forge deliberately does not hand-roll an
`EnvoyPatchPolicy` for it, since a patch pinned to Envoy's internal xDS shape
breaks silently on upgrade.
