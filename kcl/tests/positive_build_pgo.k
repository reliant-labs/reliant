"""
Positive test — a build's `pgo` (forge.PGO) renders into output.workloads[].build.

  * unset, or declared with every default: a GoBuild renders NO `pgo` key, so
    an env that declares none renders exactly as it did before PGO existed
    (the Go decoder reads an absent key as the defaults);
  * declared: every field is rendered explicitly;
  * a DockerBuild's `pgo` is keyed by binary package path, absent when empty;
  * compound whole-unit durations and the 720h ceiling are accepted.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}
_on_cluster = forge.OnCluster {target = _k3d}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [
        fw.Workload {name = "plain", image = "localhost:5050/demo", build = forge.GoBuild {cmd = "./cmd/demo"}, args = ["a"], runtime = _on_cluster}
        fw.Workload {name = "defaults", image = "localhost:5050/demo", build = forge.GoBuild {cmd = "./cmd/demo", pgo = forge.PGO {}}, args = ["b"], runtime = _on_cluster}
        fw.Workload {
            name = "tuned"
            image = "localhost:5050/tool"
            build = forge.GoBuild {cmd = "./cmd/tool", pgo = forge.PGO {from_env = "prod", since = "1h30m", min_weight = "500ms", services = ["api", "worker"]}}
            args = ["c"]
            runtime = _on_cluster
        }
        fw.Workload {name = "off", image = "localhost:5050/ctl", build = forge.GoBuild {cmd = "./cmd/ctl", pgo = forge.PGO {enabled = False, since = "720h"}}, args = ["d"], runtime = _on_cluster}
        fw.Workload {
            name = "image"
            image = "localhost:5050/image"
            build = forge.DockerBuild {pgo = {"./cmd/proxy" = forge.PGO {enabled = False}, "./cmd/demo" = forge.PGO {}}}
            runtime = _on_cluster
        }
        fw.Workload {name = "image-plain", image = "localhost:5050/image2", build = forge.DockerBuild {}, runtime = _on_cluster}
        fw.Workload {
            name = "edge"
            image = "localhost:5050/image"
            build = forge.DockerBuild {pgo = {"./cmd/proxy" = forge.PGO {enabled = False}}}
            command = ["/app/proxy"]
            runtime = _on_cluster
        }
        fw.Workload {
            name = "own-env"
            image = "localhost:5050/own"
            build = forge.GoBuild {cmd = "./cmd/own", pgo = forge.PGO {enabled = False}}
            env = {FORGE_PROFILES_PGO = "on"}
            runtime = _on_cluster
        }
        fw.Workload {
            name = "other-df"
            image = "localhost:5050/other"
            build = forge.DockerBuild {dockerfile = "svc/Dockerfile", pgo = {"./cmd/svc" = forge.PGO {enabled = False}}}
            runtime = _on_cluster
        }
    ]
})

_envof = lambda n: str -> {str:} {
    {e.name: e.value for e in ([w for w in output.workloads if w.name == n][0].spec.env or []) if "value" in e}
}
_b = {w.name: w.build for w in output.workloads}

assert_unset_renders_no_pgo = "pgo" not in _b["plain"]
assert_default_renders_no_pgo = "pgo" not in _b["defaults"]
assert_tuned_enabled = _b["tuned"].pgo.enabled == True
assert_tuned_from_env = _b["tuned"].pgo.from_env == "prod"
assert_tuned_since = _b["tuned"].pgo.since == "1h30m"
assert_tuned_min_weight = _b["tuned"].pgo.min_weight == "500ms"
assert_tuned_services = _b["tuned"].pgo.services == ["api", "worker"]
assert_off_disabled = _b["off"].pgo.enabled == False
assert_off_defaults_explicit = _b["off"].pgo.since == "720h" and _b["off"].pgo.min_weight == "10s" and _b["off"].pgo.from_env == ""
assert_docker_keyed = _b["image"].pgo["./cmd/proxy"].enabled == False and _b["image"].pgo["./cmd/demo"].enabled == True
assert_docker_empty_renders_no_pgo = "pgo" not in _b["image-plain"]

# A workload whose binary has PGO off stops collecting what only PGO reads.
assert_off_stops_start_lines = _envof("off")["FORGE_PROFILES_PGO"] == "off"
assert_docker_off_by_command = _envof("edge")["FORGE_PROFILES_PGO"] == "off"
assert_docker_off_only_key = _envof("other-df")["FORGE_PROFILES_PGO"] == "off"
assert_own_env_wins = _envof("own-env")["FORGE_PROFILES_PGO"] == "on"
assert_enabled_renders_nothing = all n in ["plain", "defaults", "tuned", "image", "image-plain"] {"FORGE_PROFILES_PGO" not in _envof(n)}
