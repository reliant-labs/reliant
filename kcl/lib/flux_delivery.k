"""
Forge KCL — gitless Flux delivery through a flux-operator ResourceSet.

WHAT THIS IS. A ledger (a control plane's deploy ledger, or forge's own file
ledger) decides WHICH bundle digest an environment runs. This lib is how that
decision becomes Flux objects: the ledger's writer puts one INPUT per ledger
BINDING (everything one promotion-row stream decides; today, one environment)
into that binding's flux-operator `ResourceSet`, and flux-operator renders
FLUX_DELIVERY_TEMPLATE into the binding's OCIRepository (digest-pinned, with
`spec.verify` when the input enforces it) and one Kustomization per applier.
A spec change on the ResourceSet is a watch event, so a promotion lands as fast
as a direct apply would, and nothing polls.

ONE TEMPLATE, TWO RENDERERS, ONE SPELLING. The template is a single string so
it has exactly one spelling. Go reads it as `forge/pkg/fluxdelivery.Template`
(template_gen.go, generated from this file and pinned to it by
TestTemplateMatchesKCL), and `fluxdelivery.Render` reproduces flux-operator's
builder in-process so a writer can check a render BEFORE it writes. The real
engine is held to the in-process one by an oracle test that runs the pinned
`flux-operator build resourceset`.

WHAT THE TEMPLATE DERIVES AND WHAT IT IS GIVEN. Object NAMES, the per-applier
PATHS and every label value are computed in Go (pkg/fluxdelivery names.go) and
arrive as inputs: a hash in the template (sprig sha256sum) would be a second
implementation of a function that must not disagree with the observer that
reads these objects back. The template owns only what is a SAFETY property of
the objects it emits:

  * `fluxcd.controlplane.io/prune: disabled` on EVERY object unless the input
    retires it. flux-operator skips an object carrying it on garbage
    collection AND on ResourceSet deletion (checked against the live object),
    so a bad render, an empty input set or a deleted ResourceSet orphans the
    environment — it freezes, it is never deleted.
  * `spec.deletionPolicy: Orphan` on every Kustomization unless the input says
    `teardown`. A Kustomization deleted by anything leaves what it applied.
  * The connected shard label, derived from the SAME field that decides
    whether a kubeConfig is rendered, so the two cannot disagree (the hub's
    admission policy refuses the mismatch; this makes it unrenderable).
  * The manifests layer, selected by media type.

THE TEMPLATE IS NOT A KNOB, as `flux_chart` has no version knob: the safety
stamps above are the reason this lib exists, and a template argument would be
a way to render without them. `flux_delivery_resourceset` takes no template.

NO SECRETS, NO RBAC IN THE TEMPLATE. Every Secret a delivery needs (the
registry pull credential, the destination kubeconfig, the verify keys) is
written by the ledger's writer, and the ResourceSet's ServiceAccount is granted
no Secret verb at all: `create` cannot be bounded by resourceNames, and a
Secret `create` in a hub namespace mints a token for any ServiceAccount there.
THE TEMPLATE RENDERS ONLY OBJECTS OWNED BY EXACTLY ONE BINDING (its
OCIRepository and its Kustomizations), on the hosted and the self-hosted path
alike. Anything shared by a namespace, an org or an environment — a Secret, a
Namespace, RBAC, a ConfigMap — is a NAME in the input: two ResourceSets
rendering one object fight over its ownership labels and garbage-collect it
from each other.

THE INPUT IS VERSIONED. Its first key is `schema`, and the template fails on
any value but the one it renders, so an input from a newer writer stalls the
ResourceSet rather than misrendering.
"""

import regex
import .flux as flux_lib

# The flux-operator API group/version of ResourceSet and its input provider.
FLUX_DELIVERY_API_VERSION = "fluxcd.controlplane.io/v1"

# The ServiceAccount a delivery ResourceSet runs as (`spec.serviceAccountName`)
# and the name of its Role and RoleBinding (`flux_delivery_rbac`). flux-operator
# impersonates it; it can touch OCIRepositories and Kustomizations in its own
# namespace and nothing else. Shared with Go (pkg/fluxdelivery.ServiceAccount).
FLUX_DELIVERY_SA = "flux-delivery"

# flux-operator's per-object garbage-collection opt-out, and its value. Shared
# with Go (pkg/fluxdelivery.PruneAnnotation / PruneDisabled).
FLUX_DELIVERY_PRUNE_ANNOTATION = "fluxcd.controlplane.io/prune"
FLUX_DELIVERY_PRUNE_DISABLED = "disabled"

# The two retire states an input may carry, only ever from a ledger-recorded
# intent. Shared with Go (pkg/fluxdelivery.RetireOrphan / RetireTeardown).
#   orphan    the appliers are suspended and stop carrying the prune opt-out,
#             with deletionPolicy Orphan: the next write that drops them (or
#             deletes the ResourceSet) removes the Flux objects and leaves
#             every workload they applied in place.
#   teardown  deletionPolicy Delete and no prune opt-out: deleting the
#             ResourceSet then deletes what the environment runs. The only
#             path on which this template ever lets a delete reach a workload.
FLUX_DELIVERY_RETIRE_ORPHAN = "orphan"
FLUX_DELIVERY_RETIRE_TEARDOWN = "teardown"

# The label a self-hosted ResourceSetInputProvider carries so the delivery
# ResourceSet's `inputsFrom` selector finds it.
FLUX_DELIVERY_INPUT_LABEL = "forge.dev/delivery"

# The resourcesTemplate. Go text/template with flux-operator's `<< >>`
# delimiters, its hermetic slim-sprig function set, and `inputs` bound to one
# input set; rendered once per input. Every scalar is written with toJson,
# which is valid YAML, so no input value can change the document's structure.
#
# A RAW STRING, and the only one in this file: TestTemplateMatchesKCL reads it
# back textually, and the evaluated value is pinned to the same bytes.
FLUX_DELIVERY_TEMPLATE = r"""<<- /* forge fluxdelivery template (kcl/lib/flux_delivery.k). One input renders one ledger binding. */ ->>
<<- $in := inputs >>
<<- if ne $in.schema "forge.dev/flux-delivery/v1" >><< fail (printf "fluxdelivery: input schema %q is not forge.dev/flux-delivery/v1, the only one this template renders" $in.schema) >><< end >>
<<- $src := $in.source >>
<<- $keep := eq $in.retire "" >>
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: << toJson $src.name >>
  namespace: << toJson $in.namespace >>
  labels:
<<- range $k, $v := $in.labels >>
    << toJson $k >>: << toJson $v >>
<<- end >>
    "sharding.fluxcd.io/key": "shard0"
  annotations:
    "deploy.reliant.dev/generation": << toJson $in.fence >>
<<- if $keep >>
    "fluxcd.controlplane.io/prune": "disabled"
<<- end >>
spec:
  url: << toJson $src.url >>
  interval: << toJson $src.interval >>
  ref:
    digest: << toJson $src.digest >>
<<- if $src.pullSecret >>
  secretRef:
    name: << toJson $src.pullSecret >>
<<- end >>
<<- if $src.insecure >>
  insecure: true
<<- end >>
<<- if $src.provider >>
  provider: << toJson $src.provider >>
<<- end >>
  layerSelector:
    mediaType: "application/vnd.forge.bundle.manifests.v1.tar+gzip"
    operation: "extract"
<<- if eq $src.verify.mode "enforce" >>
  verify:
    provider: << toJson $src.verify.provider >>
<<- if $src.verify.secretRef >>
    secretRef:
      name: << toJson $src.verify.secretRef >>
<<- end >>
<<- if $src.verify.oidc >>
    matchOIDCIdentity: << toJson $src.verify.oidc >>
<<- end >>
<<- end >>
<<- range $a := $in.appliers >>
<<- $retire := or $in.retire $a.retire >>
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: << toJson $a.name >>
  namespace: << toJson $in.namespace >>
  labels:
<<- range $k, $v := $in.labels >>
    << toJson $k >>: << toJson $v >>
<<- end >>
<<- range $k, $v := $a.labels >>
    << toJson $k >>: << toJson $v >>
<<- end >>
    "sharding.fluxcd.io/key": << if $a.kubeConfigSecret >>"connected"<< else >>"shard0"<< end >>
<<- if not $retire >>
  annotations:
    "fluxcd.controlplane.io/prune": "disabled"
<<- end >>
spec:
  interval: << toJson $src.interval >>
  prune: true
  deletionPolicy: << if eq $retire "teardown" >>"Delete"<< else >>"Orphan"<< end >>
  sourceRef:
    apiVersion: "source.toolkit.fluxcd.io/v1"
    kind: "OCIRepository"
    name: << toJson $a.source >>
  path: << toJson $a.path >>
<<- if $a.targetNamespace >>
  targetNamespace: << toJson $a.targetNamespace >>
<<- end >>
  serviceAccountName: << toJson $a.serviceAccountName >>
<<- if $a.kubeConfigSecret >>
  kubeConfig:
    secretRef:
      name: << toJson $a.kubeConfigSecret >>
      key: "value"
<<- end >>
  wait: << toJson $a.wait >>
  timeout: << toJson $a.timeout >>
<<- if $a.retryInterval >>
  retryInterval: << toJson $a.retryInterval >>
<<- end >>
<<- if or (eq $retire "orphan") (and (eq $retire "") $a.suspended) >>
  suspend: true
<<- end >>
<<- if $a.commonLabels >>
  commonMetadata:
    labels: << toJson $a.commonLabels >>
<<- end >>
<<- if $a.patches >>
  patches: << toJson $a.patches >>
<<- end >>
<<- if or $a.dependsOn $a.externalDependsOn >>
  dependsOn:
<<- range $d := $a.dependsOn >>
  - name: << toJson $d >>
<<- end >>
<<- range $d := $a.externalDependsOn >>
  - name: << toJson $d >>
<<- end >>
<<- end >>
<<- if $a.health >>
  healthChecks: << toJson $a.health >>
<<- end >>
<<- if $a.healthExprs >>
  healthCheckExprs: << toJson $a.healthExprs >>
<<- end >>
<<- end >>
"""

_LABEL_RE = r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
_SUBDOMAIN_RE = r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
_LABELS = {"app.kubernetes.io/name" = "flux-delivery", "app.kubernetes.io/managed-by" = "forge"}

# flux_delivery_resourceset — the delivery ResourceSet object.
#
#   inputs       inline inputs (`fluxdelivery.BindingInput`'s map form); one
#                ResourceSet renders one binding (`delivery.<env>`). The control-plane hub's shape: its writer owns the
#                object and every input in it.
#   inputs_from  a label selector over ResourceSetInputProviders in the same
#                namespace. The self-hosted shape: the template is a reviewed
#                bootstrap object and each deploy writes only its provider.
#
# Exactly one of the two. An input-less ResourceSet would render nothing and
# garbage-collect everything it holds — orphaned, by the prune opt-out, but an
# environment nothing reconciles any more.
flux_delivery_resourceset = lambda name: str, namespace: str, inputs: [{str:any}] = [], inputs_from: {str:any} = None, service_account: str = FLUX_DELIVERY_SA -> {str:any} {
    assert regex.match(name, _SUBDOMAIN_RE) and len(name) <= 63, "flux_delivery_resourceset: name '${name}' is not an object name that fits a label value (flux-operator stamps it on everything it applies)"
    assert regex.match(namespace, _LABEL_RE) and len(namespace) <= 63, "flux_delivery_resourceset: namespace '${namespace}' is not a namespace name"
    assert service_account, "flux_delivery_resourceset: service_account is required. A ResourceSet that names none runs as flux-operator's --default-service-account, or, in the operator's own namespace, as the operator itself"
    assert regex.match(service_account, _LABEL_RE), "flux_delivery_resourceset: service_account '${service_account}' is not a ServiceAccount name"
    assert not (inputs and inputs_from), "flux_delivery_resourceset: inputs and inputs_from are exclusive. Inline inputs are the writer-owned shape; inputs_from is the provider shape. Both would render every unit from two authorities"
    assert inputs or inputs_from, "flux_delivery_resourceset: needs inputs or inputs_from. A ResourceSet with neither renders nothing and garbage-collects what it held"
    assert not inputs_from or all k in inputs_from { k in ["matchLabels", "matchExpressions"] }, "flux_delivery_resourceset: inputs_from is a label selector (matchLabels / matchExpressions)"
    {
        apiVersion = FLUX_DELIVERY_API_VERSION
        kind = "ResourceSet"
        metadata = {name = name, namespace = namespace, labels = _LABELS}
        spec = {
            serviceAccountName = service_account
            if inputs:
                inputs = inputs
            if inputs_from:
                inputsFrom = [{
                    apiVersion = FLUX_DELIVERY_API_VERSION
                    kind = "ResourceSetInputProvider"
                    selector = inputs_from
                }]
            resourcesTemplate = FLUX_DELIVERY_TEMPLATE
        }
    }
}

# The verbs the delivery ServiceAccount holds on each Flux kind it renders.
# `delete` is the retire protocol and ResourceSet uninstall; the prune opt-out
# above is what bounds it. Shared with Go (pkg/fluxdelivery.Rules).
_FLUX_DELIVERY_VERBS = ["get", "list", "watch", "create", "update", "patch", "delete"]

# flux_delivery_rbac — the ServiceAccount, Role and RoleBinding a delivery
# ResourceSet runs as, in the namespace that holds it.
#
# NOTHING BEYOND THE TWO FLUX KINDS: no Secrets (see the header), no
# ConfigMaps, no ServiceAccounts, no RBAC, no Events.
flux_delivery_rbac = lambda namespace: str, service_account: str = FLUX_DELIVERY_SA -> [{str:any}] {
    assert regex.match(namespace, _LABEL_RE) and len(namespace) <= 63, "flux_delivery_rbac: namespace '${namespace}' is not a namespace name"
    assert service_account and regex.match(service_account, _LABEL_RE), "flux_delivery_rbac: service_account '${service_account}' is not a ServiceAccount name"
    [
        {
            apiVersion = "v1"
            kind = "ServiceAccount"
            metadata = {name = service_account, namespace = namespace, labels = _LABELS}
        }
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "Role"
            metadata = {name = service_account, namespace = namespace, labels = _LABELS}
            rules = [
                {apiGroups = ["source.toolkit.fluxcd.io"], resources = ["ocirepositories"], verbs = _FLUX_DELIVERY_VERBS}
                {apiGroups = ["kustomize.toolkit.fluxcd.io"], resources = ["kustomizations"], verbs = _FLUX_DELIVERY_VERBS}
            ]
        }
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "RoleBinding"
            metadata = {name = service_account, namespace = namespace, labels = _LABELS}
            subjects = [{kind = "ServiceAccount", name = service_account, namespace = namespace}]
            roleRef = {apiGroup = "rbac.authorization.k8s.io", kind = "Role", name = service_account}
        }
    ]
}

# ── Self-hosted delivery: the bootstrap a cluster with no control plane needs ──
#
# A Bundle with `applier = "flux"` is reconciled by a Flux that runs IN ITS OWN
# CLUSTER, installed by forge: forge records each release in its file ledger and
# points that Flux at the release's bundle, and never applies the bundle itself.
# Everything that Flux needs before the first pointer is written is one
# bootstrap platform chart, `flux_delivery_chart(...)`, which kcl/render.k adds
# to such an env's helm charts (target `FLUX_DELIVERY_CHART_NAME`):
#
#   * the Flux Operator and a hardened FluxInstance (lib/flux.k's flux_chart),
#     with the controllers' impersonation lockdown on unless the env opts out;
#   * the env's OWN DELIVERY NAMESPACE (`flux_delivery_namespace`), holding
#     everything below. One namespace per env, so two envs on one cluster are
#     two bindings that share nothing but the controllers, as the hub's
#     per-env ResourceSets are;
#   * the delivery ResourceSet `delivery.<env>`, the SAME template the hub
#     renders, reading its one input from the ResourceSetInputProvider a
#     deploy writes into that namespace (`inputs_from`), so re-applying the
#     bootstrap can never clobber the pointer;
#   * the ResourceSet's own ServiceAccount and Role (flux_delivery_rbac);
#   * three APPLY IDENTITIES (unless the env opts out). Every Kustomization the
#     template renders names one: `pre` trees apply as FLUX_DELIVERY_PRE_SA,
#     rollout and post trees as FLUX_DELIVERY_APPLY_SA. Their grants are
#     DERIVED FROM THE ENV'S RENDER in Go and ride the same chart, because
#     they depend on what the bundle contains, not on this declaration. A
#     Kustomization that names neither falls to FLUX_DELIVERY_NONE_SA, the
#     controller's default, which is granted nothing: it is Forbidden, never
#     over-privileged.
#
# The registry pull Secret is copied into the delivery namespace by the same
# bootstrap (FluxDelivery.pull_secret_from), as FLUX_DELIVERY_PULL_SECRET; the
# template only names it. A Secret rendered per input would be shared by every
# binding pulling from one registry.


# The bootstrap chart's name: the `--target` that installs it.
FLUX_DELIVERY_CHART_NAME = "flux-delivery"

# The apply identities of a self-hosted delivery, in the env's delivery
# namespace. Fixed names, because the namespace is per env: flux_chart pins the
# controller's impersonate grant to exactly these, and the grant reaches only a
# namespace that carries flux_namespace_bindings. Shared with Go
# (pkg/fluxdelivery.PreServiceAccount / ApplyServiceAccount / NoneServiceAccount).
FLUX_DELIVERY_PRE_SA = "forge-delivery-pre"
FLUX_DELIVERY_APPLY_SA = "forge-delivery-apply"
FLUX_DELIVERY_NONE_SA = "forge-delivery-none"

# The copy of the registry pull Secret the bootstrap writes into the delivery
# namespace (pkg/fluxdelivery.SelfHostedPullSecret).
FLUX_DELIVERY_PULL_SECRET = "forge-delivery-pull"

# The label every self-hosted delivery object carries with its env, so a reader
# finds one env's delivery on a shared cluster with one selector.
FLUX_DELIVERY_ENV_LABEL = "forge.dev/delivery-env"

# flux_delivery_namespace — an env's delivery namespace: project and env both,
# so two projects' `prod` on one cluster stay two bindings. Shared with Go
# (pkg/fluxdelivery.SelfHostedNamespace). The schema refuses one that is not a
# namespace name; FluxDelivery.namespace overrides it.
flux_delivery_namespace = lambda project: str, env: str -> str {
    "forge-delivery-${project}-${env}"
}

# flux_delivery_resourceset_name — the per-env ResourceSet's name, the same as
# the hub's (pkg/fluxdelivery.ResourceSetName(env, "")).
flux_delivery_resourceset_name = lambda env: str -> str {
    "delivery.${env}"
}

# flux_delivery_self_hosted — every per-env object of a self-hosted delivery
# except the derived grants (Go adds those; see the section header).
flux_delivery_self_hosted = lambda env: str, namespace: str, apply_identities: bool = True -> [any] {
    assert regex.match(namespace, _LABEL_RE) and len(namespace) <= 63, "flux_delivery_self_hosted: namespace '${namespace}' is not a namespace name (an RFC-1123 label of at most 63 characters)"
    assert regex.match(env, _LABEL_RE), "flux_delivery_self_hosted: env '${env}' is not an RFC-1123 label"
    _labels = _LABELS | {"${FLUX_DELIVERY_ENV_LABEL}" = env}
    _sa = lambda name: str -> any {
        {apiVersion = "v1", kind = "ServiceAccount", metadata = {name = name, namespace = namespace, labels = _labels}}
    }
    [
        {
            apiVersion = "v1"
            kind = "Namespace"
            metadata = {name = namespace, labels = _labels}
        }
    ] + (flux_lib.flux_namespace_bindings(namespace) if apply_identities else []) \
      + flux_delivery_rbac(namespace) \
      + ([_sa(FLUX_DELIVERY_PRE_SA), _sa(FLUX_DELIVERY_APPLY_SA), _sa(FLUX_DELIVERY_NONE_SA)] if apply_identities else []) \
      + [flux_delivery_resourceset(flux_delivery_resourceset_name(env), namespace, inputs_from = {
            matchLabels = {"${FLUX_DELIVERY_INPUT_LABEL}" = "true"}
        })]
}

# flux_delivery_chart — the bootstrap platform chart of a self-hosted delivery:
# flux_chart with the posture above and the per-env objects riding it.
#
#   apply_identities  True (default): controllers hold no cluster-wide write;
#                 every apply impersonates a derived identity. False: the
#                 distribution's own RBAC, so kustomize-controller applies as
#                 cluster-admin (a cluster nobody else shares, where Flux is the only
#                 applier) and no apply identity is rendered.
#   values / placement / distribution
#                 passed to flux_chart unchanged (the operator chart's values,
#                 a delivery node pool, a registry mirror).
flux_delivery_chart = lambda env: str, namespace: str, apply_identities: bool = True, \
        values: {str: any} = {}, placement: {str: any} = {}, distribution: {str: str} = {} -> any {
    flux_lib.flux_chart(
        values = values
        placement = placement
        distribution = distribution
        rbac = "namespaced" if apply_identities else "upstream"
        impersonate_service_account = FLUX_DELIVERY_NONE_SA
        impersonable_service_accounts = [FLUX_DELIVERY_PRE_SA, FLUX_DELIVERY_APPLY_SA]
        extra_objects = flux_delivery_self_hosted(env, namespace, apply_identities)
    ) | {name = FLUX_DELIVERY_CHART_NAME}
}
