"""
Positive tests for Bundle.lifecycle — WHO APPLIES an environment.

`lifecycle` is DECLARED, not derived, and it is about the apply path: forge
applies directly, or a reconciler converges the env from a bundle. Unset is
the meaningful default — a real environment — so an env whose author forgot
to declare it is treated as production rather than as scratch.

Exercises:

  * "local" on a cluster DECLARED in Bundle.clusters (the declarative path,
    where `forge env up` reconciles the cluster) passes and projects.
  * "local" on a `k3d-`-convention context with NO Bundle.clusters entry
    passes. This is the path the SCAFFOLDED dev env takes: its cluster comes
    from deploy/k3d.yaml via `forge cluster up`, so it is never a
    Bundle.clusters entry, and a check that demanded one would refuse the
    env forge itself scaffolds.
  * "ephemeral" projects, and is a distinct value from "local".
  * An env that declares nothing projects `lifecycle: null` — a stable key,
    so the Go consumer reads the default rather than a missing field.
  * A HOST-ONLY env may declare local: it targets no cluster at all.
  * The declaration does NOT leak into the manifest stream — it is a
    host-side apply-path fact, not a Kubernetes object or a label.
"""

import forge
import forge.workloads as fw

# ── Declared cluster: the Bundle.clusters path ──────────────────────────

_declared = forge.Cluster {
    name = "dev"
}

_on_declared = forge.ClusterTarget {
    cluster = "k3d-dev"
    namespace = "fixture-dev"
}

_declared_bundle = forge.Bundle {
    project = "fixture"
    lifecycle = "local"
    clusters = [_declared]
    cluster_target = _on_declared
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5051/api"
        runtime = forge.OnCluster {target = _on_declared}
    }]
}

# ── k3d convention, NO Bundle.clusters: the scaffolded dev env's shape ──

_scaffold_target = forge.ClusterTarget {
    cluster = "k3d-fixture"
    namespace = "fixture-dev"
}

_scaffold_bundle = forge.Bundle {
    project = "fixture"
    lifecycle = "local"
    cluster_target = _scaffold_target
    workloads = [fw.Workload {
        name = "api"
        image = "localhost:5050/fixture"
        runtime = forge.OnCluster {target = _scaffold_target}
    }]
}

# ── Ephemeral: a throwaway per-run cluster ──────────────────────────────

_ephemeral_bundle = forge.Bundle {
    project = "fixture"
    lifecycle = "ephemeral"
    clusters = [forge.Cluster {name = "e2e"}]
    cluster_target = forge.ClusterTarget {
        cluster = "k3d-e2e"
        namespace = "fixture-e2e"
    }
}

# ── Undeclared: a REAL env, reconciled from a bundle ────────────────────

_real_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = forge.ClusterTarget {
        cluster = "gke_acme_us-central1_prod"
        namespace = "acme-prod"
    }
}

# ── Host-only: nothing to apply, so the claim is vacuous but allowed ────

_host_bundle = forge.Bundle {
    project = "fixture"
    lifecycle = "local"
    workloads = [fw.Workload {
        name = "api"
        build = forge.GoBuild {cmd = "./cmd/api"}
        runtime = forge.OnHost {runner = "go-run"}
    }]
}

output = forge.render(_declared_bundle)
output_scaffold = forge.render(_scaffold_bundle)
output_ephemeral = forge.render(_ephemeral_bundle)
output_real = forge.render(_real_bundle)
output_host = forge.render(_host_bundle)
manifests = forge.render(_declared_bundle).manifests

# ── Invariants ─────────────────────────────────────────────────────

# A cluster declared in Bundle.clusters: the declaration projects.
assert_declared = output.lifecycle == "local"

# The scaffolded shape — k3d convention, no Bundle.clusters entry — is
# accepted, and projects the same value.
assert_scaffold = output_scaffold.lifecycle == "local"

# "ephemeral" is its own value, carried through verbatim.
assert_ephemeral = output_ephemeral.lifecycle == "ephemeral"

# Unset projects a stable null, NOT a missing key: the Go consumer reads
# the default rather than having to distinguish absent from empty.
assert_real_null = output_real.lifecycle == None

# A host-only env targets no cluster, so it may declare local.
assert_host = output_host.lifecycle == "local"

# The declaration is host-side only. It is not a k8s object, and it is not
# stamped onto one — nothing in the applyable stream mentions it.
assert_no_manifest_leak = len([m for m in manifests if "lifecycle" in str(m)]) == 0
