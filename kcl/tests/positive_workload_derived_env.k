"""
Positive test — the env keys forge derives for a key the author left unset.

OTEL_SERVICE_NAME is the workload's NAME on every runtime that runs a
process. A project-wide value stamped onto every workload ("unknown" in
control-plane's prod, from a config-proto default) filed every service's
spans and errors under one name; the workload name is unique per env by
construction. An author's own non-empty value wins; an EMPTY literal is "no
value" to the process and takes the derived one.

GOMEMLIMIT is go_memory_limit_percent of the container memory limit, in
whole MiB, for a Go process on a runtime that HAS a container limit
(cluster, hosted):

  * a forge.GoBuild workload defaults to 90%: 2Gi -> 1843MiB (never above
    the limit);
  * an unset limit is the request (v1alpha1 WithDefaults), and an unset
    request is the 1Gi default;
  * a non-GoBuild workload gets none unless it declares the percent;
  * 0 opts out; GOMEMLIMIT or GOGC in `env` opts out;
  * host and compose have no container limit to derive from;
  * a tool runs no process.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_on_cluster = forge.OnCluster {target = _k3d}
_go = forge.GoBuild {cmd = "./cmd/demo", output_name = "demo"}
_two_gib = fw.Resources {memoryRequestBytes = forge.mib(512), memoryLimitBytes = forge.gib(2)}

output = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [
        # GoBuild, 2Gi limit: 90% of 2048MiB.
        fw.Workload {name = "api", image = "localhost:5050/demo", build = _go, args = ["api"], resources = _two_gib, runtime = _on_cluster}
        # GoBuild, request only: the request is the limit.
        fw.Workload {name = "request-only", image = "localhost:5050/demo", build = _go, args = ["x"], resources = fw.Resources {memoryRequestBytes = forge.mib(1000)}, runtime = _on_cluster}
        # GoBuild, no resources: the 1Gi default request.
        fw.Workload {name = "defaults", image = "localhost:5050/demo", build = _go, args = ["y"], runtime = _on_cluster}
        # A sibling repo's Go image: forge cannot see it is Go, so it says so.
        fw.Workload {name = "sibling", image = "localhost:5050/reliant", args = ["server"], resources = _two_gib, go_memory_limit_percent = 80, runtime = _on_cluster}
        # A third-party image: no GOMEMLIMIT, but still a service name.
        fw.Workload {name = "third-party", image = "docker.io/library/nats:2", resources = _two_gib, runtime = _on_cluster}
        # Opted out, and the author's own service name wins.
        fw.Workload {name = "opted-out", image = "localhost:5050/demo", build = _go, args = ["z"], go_memory_limit_percent = 0, env = {OTEL_SERVICE_NAME = "custom"}, runtime = _on_cluster}
        # Tunes its own GC: forge derives no GOMEMLIMIT. A blank service name
        # takes the derived one, in place.
        fw.Workload {name = "own-gc", image = "localhost:5050/demo", build = _go, args = ["w"], env = {OTEL_SERVICE_NAME = "", GOGC = "50"}, runtime = _on_cluster}
        fw.Workload {name = "own-limit", image = "localhost:5050/demo", build = _go, args = ["v"], env = {GOMEMLIMIT = "100MiB"}, runtime = _on_cluster}
        # Hosted has a container limit too.
        fw.Workload {name = "hosted", image = "ghcr.io/acme/demo", build = _go, resources = _two_gib, ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnHosted {}}
        # A host process has no container limit.
        fw.Workload {name = "local", build = _go, args = ["api"], runtime = forge.OnHost {}}
        # A tool runs nothing.
        fw.Workload {name = "cli", kind = "tool", build = _go, image = "localhost:5050/demo", args = ["admin"], runtime = _on_cluster}
    ]
})

_envof = lambda n: str -> {str:} {
    {e.name: e.value for e in ([w for w in output.workloads if w.name == n][0].spec.env or []) if "value" in e}
}
_recenv = lambda n: str -> {str:} {
    {e.name: e.value for e in [m for m in output.manifests if m.kind == "Workload" and m.metadata.name == n][0].spec.env if "value" in e}
}

assert_api_service_name = _envof("api")["OTEL_SERVICE_NAME"] == "api"
assert_api_gomemlimit = _envof("api")["GOMEMLIMIT"] == "1843MiB"
assert_record_matches_entry = _recenv("api") == _envof("api")
assert_request_is_limit = _envof("request-only")["GOMEMLIMIT"] == "900MiB"
assert_default_request = _envof("defaults")["GOMEMLIMIT"] == "921MiB"
assert_declared_percent = _envof("sibling")["GOMEMLIMIT"] == "1638MiB"
assert_third_party_no_gomemlimit = "GOMEMLIMIT" not in _envof("third-party")
assert_third_party_service_name = _envof("third-party")["OTEL_SERVICE_NAME"] == "third-party"
assert_opt_out = "GOMEMLIMIT" not in _envof("opted-out")
assert_author_service_name_wins = _envof("opted-out")["OTEL_SERVICE_NAME"] == "custom"
assert_gogc_owns_gc = "GOMEMLIMIT" not in _envof("own-gc")
assert_blank_service_name_derived = _envof("own-gc")["OTEL_SERVICE_NAME"] == "own-gc"
assert_blank_keeps_position = [e.name for e in [w for w in output.workloads if w.name == "own-gc"][0].spec.env] == ["OTEL_SERVICE_NAME", "GOGC", "OTEL_RESOURCE_ATTRIBUTES"]
assert_own_limit_kept = _envof("own-limit")["GOMEMLIMIT"] == "100MiB"
assert_hosted_gomemlimit = _envof("hosted")["GOMEMLIMIT"] == "1843MiB"
assert_host_no_gomemlimit = "GOMEMLIMIT" not in _envof("local")
assert_host_service_name = _envof("local")["OTEL_SERVICE_NAME"] == "local"
assert_tool_nothing = _envof("cli") == {}
