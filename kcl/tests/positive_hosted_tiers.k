"""
Positive test — HOSTED workloads and a hosted ManagedDatabase.

A workload bound to forge.OnHosted {} is published to the control plane as a
Workload CR; the platform allocates its namespace and renders it. So:

  1. output.workloads[] carries it with runtime.type == "hosted" and its
     WorkloadSpec — the spec the hosted provider publishes.
  2. output.manifests has NO Workload record for it: forge applies nothing
     to any cluster of its own for a hosted workload.
  3. A hosted job is published too (it used to be silently ignored).
  4. The database follows the same rule per database.
"""

import forge
import forge.workloads as fw

_img = "ghcr.io/acme/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

_bundle = forge.Bundle {
    project = "acme"
    env = "prod"
    control_plane = forge.ControlPlane {endpoint = "https://api.example.com"}
    workloads = [_w | {runtime = forge.OnHosted {}} if not _w.runtime else _w for _w in [
        fw.Workload {
            name = "api"
            image = _img
            ports = [fw.Port {name = "http", port = 8080, expose = True}]
            env = {
                DATABASE_URL = forge.DatabaseRef {name = "orders"}
                STRIPE_KEY = forge.ManagedSecret {name = "STRIPE_KEY"}
            }
            storageGiB = 5
        }
        fw.Workload {name = "migrate", kind = "job", image = _img, args = ["db", "migrate", "up"]}
    ]]
    databases = [forge.ManagedDatabase {name = "orders", runtime = forge.OnHosted {}}]
}

output = forge.render(_bundle)
_w = {w.name: w for w in output.workloads}

assert_runtime_hosted = _w["api"].runtime.type == "hosted"
# OnHosted carries no coordinates: the platform owns them.
assert_hosted_block_is_bare = len([k for k in _w["api"].runtime if k not in ["type", "platform"]]) == 0
assert_spec_image_verbatim = _w["api"].spec.image == _img
assert_spec_ports = [p.port for p in _w["api"].spec.ports] == [8080]
assert_spec_storage = _w["api"].spec.storageGiB == 5
# Restricted-legal env channels ride the spec unchanged for the platform.
_env = {e.name: e for e in _w["api"].spec.env}
assert_database_ref = _env["DATABASE_URL"].databaseRef.name == "orders"
assert_managed_secret = _env["STRIPE_KEY"].managedSecret.name == "STRIPE_KEY"

assert_hosted_job_published = _w["migrate"].runtime.type == "hosted" and _w["migrate"].kind == "job"

assert_no_records = len([m for m in output.manifests if m.kind in ["Workload", "ManagedDatabase"]]) == 0
assert_db_entity = output.databases[0].name == "orders" and output.databases[0].runtime == "hosted"
assert_control_plane = output.control_plane.endpoint == "https://api.example.com"
