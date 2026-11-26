"""
Positive test — a ShellBuild's `pgo` (forge.ShellPGO, keyed by the binary's
package path relative to cwd) renders into output.workloads[].build.

  * unset: a ShellBuild renders NO `pgo` key, so an env that declares none
    renders exactly as it did before ShellBuild.pgo existed;
  * declared: every field is rendered explicitly, `out` and `namespace`
    included (empty when left to the default);
  * a `source` build accepts `pgo` when every binary names its `out`;
  * a ShellBuild's disabled PGO renders no FORGE_PROFILES_PGO: forge does not
    see which binary a shell workload runs, nor edit the command's `-pgo`.
"""

import forge
import forge.workloads as fw

_on_cluster = forge.OnCluster {target = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}}
_reliant = forge.ShellBuild {
    cwd = "../reliant"
    cmd = "./scripts/build-image.sh"
    pgo = {"./cmd/reliant" = forge.ShellPGO {from_env = "prod", services = ["reliant-api-server"]}}
}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [
        fw.Workload {name = "plain", image = "localhost:5050/plain", build = forge.ShellBuild {cmd = "true"}, runtime = _on_cluster}
        fw.Workload {name = "reliant-api-server", image = "localhost:5050/reliant", build = _reliant, args = ["server", "api"], runtime = _on_cluster}
        fw.Workload {
            name = "tuned"
            image = "localhost:5050/tuned"
            build = forge.ShellBuild {
                cmd = "make image"
                pgo = {
                    "./cmd/a" = forge.ShellPGO {out = "../profiles/a.pgo", namespace = "other", since = "72h", min_weight = "500ms"}
                    "./cmd/b" = forge.ShellPGO {enabled = False}
                }
            }
            runtime = _on_cluster
        }
        fw.Workload {
            name = "pinned"
            image = "localhost:5050/pinned"
            build = forge.ShellBuild {
                source = forge.GitSource {repo = "github.com/acme/api", ref = "v1.2.3"}
                cmd = "make image"
                pgo = {"./cmd/api" = forge.ShellPGO {out = "profiles/api/default.pgo"}}
            }
            runtime = _on_cluster
        }
    ]
})

_envof = lambda n: str -> {str:} {
    {e.name: e.value for e in ([w for w in output.workloads if w.name == n][0].spec.env or []) if "value" in e}
}
_b = {w.name: w.build for w in output.workloads}

assert_unset_renders_no_pgo = "pgo" not in _b["plain"]
assert_keyed_by_cwd_package = [k for k in _b["reliant-api-server"].pgo] == ["./cmd/reliant"]
_r = _b["reliant-api-server"].pgo["./cmd/reliant"]
assert_declared_fields = _r.enabled == True and _r.from_env == "prod" and _r.services == ["reliant-api-server"] and _r.since == "168h" and _r.min_weight == "10s"
assert_defaults_render_empty_out_and_namespace = _r.out == "" and _r.namespace == ""
_a = _b["tuned"].pgo["./cmd/a"]
assert_out_and_namespace = _a.out == "../profiles/a.pgo" and _a.namespace == "other" and _a.since == "72h" and _a.min_weight == "500ms"
assert_disabled = _b["tuned"].pgo["./cmd/b"].enabled == False
assert_source_with_out = _b["pinned"].pgo["./cmd/api"].out == "profiles/api/default.pgo"
assert_shell_renders_no_profiles_env = "FORGE_PROFILES_PGO" not in _envof("tuned")
