"""
Closed-schema test — `raw_policy` is GONE from HTTPRoute (ADR-0003 F2).

It was documented as "emitted verbatim alongside the route" and nothing
in forge ever emitted it: the field existed in the schema, the JSON
serializer and the Go struct, and was read by no renderer. A field that
silently does nothing is worse than a missing one — it reads as
configuration that is in effect.

Pre-1.0, so it is DELETED rather than deprecated, and replaced by the
typed `traffic` (RouteTraffic), which renders a real Envoy Gateway
BackendTrafficPolicy. Setting it must now be a hard compile error, not a
silently ignored attribute.
"""

# expect: raw_policy

import forge

_r = forge.HTTPRoute {
    name = "api"
    gateway = "public"
    listener = "http"
    service = "api"
    port = 8080
    raw_policy = "apiVersion: gateway.envoyproxy.io/v1alpha1\nkind: BackendTrafficPolicy\n"
}
