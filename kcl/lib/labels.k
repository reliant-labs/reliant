"""
Forge KCL — the ENV-OWNERSHIP stamp.

Every k8s object forge renders carries `forge.dev/env: <env>` so a
deployed object can answer, on its own, WHICH ENVIRONMENT PUT IT THERE.

Why this exists
===============
Two forge envs can legitimately resolve to the SAME namespace (a
`dev` and a `dev-k8s` that both render into `control-plane-dev`). When
they do, deploying one silently replaces the other's workloads:
`kubectl apply` sees the same object names and overwrites them. Nothing
on the resulting objects records which env's render won, so untangling
it afterwards is archaeology — the incident this label closes cost ~10h
of "no machine is connected" because a daemon-gateway had been repointed
at another env's database.

With the stamp the question is one selector:

    kubectl get all -n <ns> -l 'forge.dev/env!=dev'   # what dev does NOT own

`forge.dev/*` is forge's own label namespace — the sibling
`forge.dev/cluster` routing label (lib/gateway.k) already established
it. The `app.kubernetes.io/*` set is deliberately left alone: those keys
have k8s-recommended meanings (name / part-of / managed-by) and are
load-bearing for forge's `--target` selection and multi-cluster routing.

Where the value comes from
==========================
1. An explicit KCL DRILL-IN — `Bundle.env` (schema.k) or
   `WorkloadEnv.env` (workloads/render.k). A consumer that wants a
   different ownership tag than the env it renders under just sets the
   field; there is no CLI flag and no new render option.
2. Otherwise `option("env")` — the `-D env=<name>` binding the forge CLI
   passes on EVERY manifest render (internal/cluster.renderDArgs). This
   is what makes the stamp automatic: a project gets it without editing
   a line of KCL.
3. Otherwise NOTHING. A plain `kcl run` with no `-D env=` binding and no
   drill-in emits no label at all, so the module's own fixtures — and
   any hand render — stay BYTE-IDENTICAL to the pre-stamp output.

Where the stamp is applied
==========================
At the render ENTRY POINTS (`forge.render_manifests`,
`forge.render_support`, `forge.workloads.render_workloads`), over the
FINAL manifest list. That placement — rather than threading an `env`
argument through every builder — is what makes the guarantee total: it
also covers `Bundle.manifests (forge.Manifests)`, hand-written raw dicts, the
NetworkPolicy bundle and CRDs, none of which pass through a forge
label builder at all.

WHAT IT WRITES, AND WHAT IT PROVABLY DOES NOT
=============================================
`stamp_env` writes to exactly two places, both LABEL maps:

  * `metadata.labels` — universally legal on every k8s object,
    namespaced or cluster-scoped, and always mutable.
  * `spec.template.metadata.labels` (pod template) for the kinds in
    `_POD_TEMPLATE_KINDS`, and `spec.jobTemplate.spec.template.metadata
    .labels` for a CronJob. Gated on `kind` so the walk can never
    stumble into an unrelated CRD field that happens to be spelled
    `spec.template`.

It NEVER touches a selector. `spec.selector` (Deployment /
StatefulSet / DaemonSet `matchLabels`, and Service's plain map) is
IMMUTABLE on workload controllers — adding a key there makes
`kubectl apply` fail with "field is immutable" on every object that
already exists — and on a Service it silently detaches endpoints.
Every forge builder derives its selector from a SEPARATE one-key
literal (`{"app.kubernetes.io/name" = <name>}`), never from the label
map this stamp merges into, so the two cannot converge. The rebuild
below is explicit about it: it unions a new `template` onto the
EXISTING `spec`, so `spec.selector`, `spec.replicas` and everything
else survive byte-identically.

Adding a pod-template label DOES roll the Deployment (a new
ReplicaSet), which is the normal, intended cost: without it, pods carry
no env tag and `-l 'forge.dev/env!=dev'` matches every pod in the
namespace, which is exactly the noise the label is meant to remove.
"""

# The wire key. Consumers select on it (`-l forge.dev/env=<env>`), so it
# is a stable part of forge's contract — not an implementation detail.
ENV_LABEL_KEY: str = "forge.dev/env"

# Kinds whose `spec.template` IS a PodTemplateSpec. The stamp reaches a
# pod template ONLY for these, so a CRD with an unrelated `spec.template`
# field is never rewritten. CronJob is handled separately (its pod
# template hangs off `spec.jobTemplate.spec.template`).
#
# A Job's `spec.template` is immutable once created, so stamping it makes
# a warm re-apply of an EXISTING Job fail with "field is immutable" —
# exactly as a changed image tag already does. forge's deploy path
# already recovers from that (internal/cluster.go: delete → wait-gone →
# re-apply, `immutableRecoveryAttempts`), so the Job stays in scope
# rather than being silently left untagged.
_POD_TEMPLATE_KINDS: [str] = ["Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job"]

# Resolve the env-label VALUE: explicit drill-in wins, else the `-D env=`
# binding the CLI passes, else "" (meaning: emit no label).
env_value = lambda env: str = "" -> str {
    env if env else (option("env") or "")
}

# The env label as a merge-ready map. Empty when no env resolves, so
# every caller degrades to a no-op rather than emitting `forge.dev/env: ""`.
env_labels = lambda env: str = "" -> {str: str} {
    _v = env_value(env)
    {"${ENV_LABEL_KEY}" = _v} if _v else {}
}

# Merge `add` into a metadata block's `labels`, UNDER whatever labels are
# already there: a key the author set explicitly WINS and is left exactly
# as written (the same precedence owner_labels gives the app.kubernetes.io set).
# Only genuinely-new keys are appended, and they are appended LAST, so the
# rendered label block diffs as one added line rather than a reordering.
# Everything else on `metadata` (name, namespace, annotations,
# ownerReferences, …) is carried through untouched.
_merge_labels = lambda meta: any, add: {str: str} -> any {
    _m = meta if meta else {}
    _existing = _m.labels if _m.labels else {}
    _m | {labels = _existing | {k: add[k] for k in add if not (k in _existing)}}
}

# Stamp ONE manifest. Kind-agnostic for `metadata.labels`; kind-GATED for
# pod templates. See the module docstring for the selector-safety
# argument. Returns the manifest untouched when no env resolves.
stamp_env = lambda manifest: any, env: str = "" -> any {
    _add = env_labels(env)
    _kind = manifest.kind if manifest.kind else ""
    _spec = manifest.spec if manifest.spec else {}

    # Object metadata — every object, every kind, always legal.
    _obj = manifest | {metadata = _merge_labels(manifest.metadata if manifest.metadata else {}, _add)}

    # Pod template. `_tmpl` is resolved only for the gated kinds, so a
    # CRD whose `spec.template` means something else is never read.
    _tmpl = (_spec.template if _spec.template else {}) if _kind in _POD_TEMPLATE_KINDS else {}
    _pod = (_obj | {
        spec = (_spec | {
            template = (_tmpl | {metadata = _merge_labels(_tmpl.metadata if _tmpl.metadata else {}, _add)})
        })
    }) if _tmpl else _obj

    # CronJob pod template (spec.jobTemplate.spec.template). Same rebuild,
    # one level deeper; `spec.schedule` and the jobTemplate's own spec
    # keys ride through the unions unchanged.
    _jt = (_spec.jobTemplate if _spec.jobTemplate else {}) if _kind == "CronJob" else {}
    _jt_spec = _jt.spec if _jt.spec else {}
    _jt_tmpl = _jt_spec.template if _jt_spec.template else {}
    _cron = (_obj | {
        spec = (_spec | {
            jobTemplate = (_jt | {
                spec = (_jt_spec | {
                    template = (_jt_tmpl | {metadata = _merge_labels(_jt_tmpl.metadata if _jt_tmpl.metadata else {}, _add)})
                })
            })
        })
    }) if _jt_tmpl else _pod

    _cron if _add else manifest
}

# Stamp a whole manifest list. The form the render entry points call.
stamp_env_all = lambda manifests: [any], env: str = "" -> [any] {
    [stamp_env(m, env) for m in manifests]
}

# ---------------------------------------------------------------------------
# The forge-managed `app.kubernetes.io/*` set.
#
# ONE renderer shared by every manifest builder (lib/services.k, lib/rbac.k,
# lib/gateway.k, workloads/expand.k). It used to be a private `_managed_labels`
# copied into each builder, and the copies drifted: lib/services.k hardcoded
# `part-of = name`, so `kubectl get deployments -l app.kubernetes.io/part-of=<project>`
# matched nothing and a CD "wait for rollouts" step iterated an EMPTY selector
# and passed vacuously while deployments never rolled.
#
# `app.kubernetes.io/name` is THIS object's workload; `app.kubernetes.io/part-of`
# is the PROJECT it belongs to — the handle that selects every workload of one
# release. Collapsing them is precisely that defect. `part_of` empty falls back
# to `name`, so a caller with no project context still emits a well-formed (if
# ungrouped) label set.
# ---------------------------------------------------------------------------
managed_labels = lambda name: str, part_of: str = "" -> {str: str} {
    {
        "app.kubernetes.io/name" = name
        "app.kubernetes.io/managed-by" = "forge"
        "app.kubernetes.io/part-of" = part_of if part_of else name
    }
}

# ---------------------------------------------------------------------------
# The OWNER stamp for manifests forge did not build.
#
# `forge.dev/workload` is forge's ROUTING key: the deploy GROUP that
# `forge env deploy --target` selects and the multi-cluster router
# (internal/cluster.ScopeManifestsToGroup) attributes a document by. Go reads it
# through cluster.ManifestGroup, which falls back to `app.kubernetes.io/name`
# for the objects forge's own builders render (those set both to the same
# value, so they carry no second label and no pod rolls).
#
# Why a forge-owned key instead of `app.kubernetes.io/name`. A raw manifest
# attached to a workload (`RenderedWorkload.manifests`) is very often chart
# output or a hand-written controller, and its `app.kubernetes.io/name` means
# something to ITSELF: a Service or DaemonSet selector, a ServiceMonitor, an
# operator that looks its own objects up by label. Overwriting it to route the
# object would silently break whatever keys on it; deferring to it (the old
# behaviour) let the manifest's own label decide which `--target` shipped it,
# so control-plane's kata pre-pull DaemonSet — labelled for its selector —
# was dropped from `--target kata-prepull`. Routing by a key nothing else
# reads removes the conflict instead of choosing a loser.
#
# The rule, per label:
#   * `forge.dev/workload` — ALWAYS the owner. forge owns this key.
#   * `app.kubernetes.io/{name,managed-by,part-of}` — forge's value is a
#     DEFAULT, written only when the manifest carries none. An existing value
#     is left exactly as written, whatever it is.
#   * every other label — carried through untouched.
#
# Built by comprehension, never by `defaults | existing`: a dict decoded by
# `yaml.decode` unions with `:` (unify) semantics, so `{managed-by = "forge"}
# | {managed-by: "Helm"}` is a KCL "conflicting values" error that fails the
# whole render. Each key below is produced by exactly one of three disjoint
# comprehensions, so no key is ever unified.
# ---------------------------------------------------------------------------
WORKLOAD_LABEL_KEY: str = "forge.dev/workload"

owner_labels = lambda existing: {str: str}, name: str, part_of: str = "" -> {str: str} {
    _defaults = managed_labels(name, part_of)
    # Defaults first, in their usual order, so an unlabelled manifest renders
    # its label block exactly as it did before the owner key existed.
    {k: existing[k] if k in existing else _defaults[k] for k in _defaults} \
        | {k: existing[k] for k in existing if not (k in _defaults) and k != WORKLOAD_LABEL_KEY} \
        | {"${WORKLOAD_LABEL_KEY}" = name}
}
