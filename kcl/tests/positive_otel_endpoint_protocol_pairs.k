"""
Positive test — an OTLP endpoint is never rendered without the protocol that
speaks to it. The runtime defaults to http/protobuf (:4318); an endpoint on
the gRPC port (:4317) with no OTEL_EXPORTER_OTLP_PROTOCOL=grpc exports
nothing, silently. forge.OTEL_ENV is the default a project splices into a
workload that wires its own collector address.

The default used to be `otel-collector.observability:4317`, a service nothing
deployed. It is now the node agent forge renders (Observability.agent), on its
OTLP/HTTP port, with the protocol that speaks to that port.
"""

import forge

_env = {e.name: e.value for e in forge.OTEL_ENV if e.value}

assert_default_endpoint_is_the_node_agent = _env.OTEL_EXPORTER_OTLP_ENDPOINT == "http://otel-agent.forge-observability.svc.cluster.local:4318"
assert_http_port_carries_http_protocol = _env.OTEL_EXPORTER_OTLP_PROTOCOL == "http/protobuf"
assert_no_stale_collector_address = "otel-collector.observability" not in _env.OTEL_EXPORTER_OTLP_ENDPOINT
