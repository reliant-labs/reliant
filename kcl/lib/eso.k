"""
Forge KCL — the External Secrets Operator, hardened, one per cluster.

`forge.ExternalSecretsOperator {cluster = c}` renders as ONE forge.HelmChart
(`external-secrets`, `--target external-secrets`) bound to the cluster it names,
a k3d `forge.Cluster` or any `forge.ClusterTarget`. This file is that
lowering. Everything below follows ESO's security guide
(https://external-secrets.io/v1.1.0/guides/security-best-practices/); the
residual risk that remains (the controller reads and writes every Secret on its
cluster) is stated in the `deploy/external-secrets` skill.

WHAT IS RENDERED, AND WHAT IS NOT

  * The chart is rendered `--skip-crds` AND with `installCRDs: false`. In chart
    2.13 the CRDs are ordinary templates gated on `installCRDs`, so
    `--skip-crds` alone would still emit them. forge supplies the three CRDs
    the install uses (ExternalSecret, SecretStore, GeneratorState) from its own
    bundle (`crds = "external-secrets"`, pkg/deploy/eso), applied first and
    waited Established. ClusterSecretStore, ClusterExternalSecret, PushSecret,
    ClusterPushSecret, ClusterGenerator and every generator have NO CRD on the
    cluster, so nothing can create one.
  * The webhook and the cert controller are off. P1: chart 2.13 sets no
    `spec.conversion` on any CRD (conversion is off, and the CRD bundle serves
    only v1), so the API server never calls the webhook that is not there.
    With `crds.conversion.enabled = false`, `webhook.create = false` is the
    chart's own documented pairing. The VALIDATING webhook is replaced by the
    admission policies below, which the API server evaluates in-process.
  * `rbac.serviceAccountTokenCreate` is off: the operator can mint a token for
    no ServiceAccount until a Role with `resourceNames` says so (SF5 renders
    one per SecretStore namespace). `rbac.aggregateTo{View,Edit,Admin}` and the
    servicebindings ClusterRole are off, so no namespace admin inherits access
    to ExternalSecrets or SecretStores.
  * The namespace is Pod Security `restricted` (enforced); the pod runs
    non-root with a read-only root filesystem, seccomp RuntimeDefault, no
    privilege escalation and every capability dropped (the chart's defaults,
    pinned by a test). The image is pinned BY DIGEST.
  * A NetworkPolicy denies everything but DNS, the API server and the store
    (egress), and metrics scrapes from named namespaces (ingress).
  * The controller does not cache Secrets or ConfigMaps cluster-wide; it holds
    informers only for Secrets it manages.

THE THREE ADMISSION POLICIES (ValidatingAdmissionPolicy, evaluated in-process
by the API server, so there is no webhook to be down)

    eso-writers     who may create, update or delete an ExternalSecret or a
                    SecretStore: the declared writers, the operator, and the
                    cluster's own garbage collector.
    eso-store-pin   what a SecretStore may point at: only the declared servers,
                    JWT mounts, audiences and service account.
    eso-es-policy   what an ExternalSecret may ask for: no dataFrom, creation
                    policy Orphan or Merge, deletion policy Retain, its own
                    name as the target, a refresh of at least five minutes.

All three fail closed (`failurePolicy: Fail`, `Deny`). Each is a named, cluster-
scoped object, so they are inspectable with `kubectl get validatingadmission
policies`. An UPDATE that leaves `spec` alone (the operator's finalizers and
annotations) passes the two input policies, so tightening a policy can never
wedge a deletion.

THE SEAMS. `ExternalSecretsOperator.values` merges over the posture, last wins;
overriding a hardening value is the 20% path and nothing refuses it. The
admission lists are typed (`ExternalSecretsOperator.admission`).
"""

import regex

# The chart. Shared with Go (pkg/deploy/eso) and pinned by a test.
ESO_CHART_OCI = "oci://ghcr.io/external-secrets/charts/external-secrets"
ESO_CHART_NAME = "external-secrets"
# The Helm release name is the chart name, so the controller's ServiceAccount
# and every object keep the chart's own short names. Pinned on the SA below.
ESO_SERVICE_ACCOUNT = "external-secrets"
ESO_COMPONENT_OWNER_LABEL = "forge.dev/component-owner"
ESO_COMPONENT_LABEL = "forge.dev/component"
ESO_COMPONENT = "external-secrets-operator"

# The guide's ECDHE-CHACHA20 set (§5).
ESO_TLS_CIPHERS = "TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256"

# The cluster's own controllers that write ESO objects: garbage collection of
# an orphaned ExternalSecret and a namespace being torn down.
_CLUSTER_CONTROLLERS = [
    "system:serviceaccount:kube-system:generic-garbage-collector"
    "system:serviceaccount:kube-system:namespace-controller"
]

_ESO_GROUPS = "external-secrets.io"

# eso_owner(project, env) — the component-owner label value, spelled the way
# the Go side (cluster.CRDOwner) spells it.
eso_owner = lambda project: str, env: str -> str {
    _v = regex.replace(project + "." + env, "[^A-Za-z0-9._-]", "-")
    regex.replace(_v[:63], "^[._-]+|[._-]+$", "")
}

# A CEL list literal of strings. Every element was validated to carry no quote
# or backslash (ESOAdmission), so it cannot close the literal.
_cel_list = lambda xs: [str] -> str {
    "[" + ", ".join(["'${x}'" for x in xs]) + "]"
}

_labels = lambda owner: str -> {str: str} {
    {
        "${ESO_COMPONENT_LABEL}" = ESO_COMPONENT
        "${ESO_COMPONENT_OWNER_LABEL}" = owner
        "app.kubernetes.io/managed-by" = "forge"
    }
}

_psa = lambda -> {str: str} {
    {
        "pod-security.kubernetes.io/enforce" = "restricted"
        "pod-security.kubernetes.io/enforce-version" = "latest"
        "pod-security.kubernetes.io/warn" = "restricted"
        "pod-security.kubernetes.io/audit" = "restricted"
    }
}

# ---------------------------------------------------------------------------
# Admission policies
# ---------------------------------------------------------------------------

_vap = lambda name: str, owner: str, resources: [str], operations: [str], \
        validations: [{str: str}] -> [any] {
    [
        {
            apiVersion = "admissionregistration.k8s.io/v1"
            kind = "ValidatingAdmissionPolicy"
            metadata = {name = name, labels = _labels(owner)}
            spec = {
                failurePolicy = "Fail"
                matchConstraints = {
                    resourceRules = [{
                        apiGroups = [_ESO_GROUPS]
                        apiVersions = ["*"]
                        operations = operations
                        resources = resources
                    }]
                }
                validations = [{expression = v.expression, message = v.message, reason = "Forbidden"} for v in validations]
            }
        }
        {
            apiVersion = "admissionregistration.k8s.io/v1"
            kind = "ValidatingAdmissionPolicyBinding"
            metadata = {name = name, labels = _labels(owner)}
            spec = {policyName = name, validationActions = ["Deny"]}
        }
    ]
}

# A spec-neutral UPDATE (finalizers, annotations, the operator's bookkeeping)
# is never judged by the input policies.
_spec_unchanged = "(request.operation == 'UPDATE' && object.spec == oldObject.spec)"

# eso-writers: who may write ExternalSecrets and SecretStores.
_writers_vap = lambda owner: str, eso_user: str, writers: [str], groups: [str] -> [any] {
    _users = [eso_user] + _CLUSTER_CONTROLLERS + writers
    _vap("eso-writers", owner, ["externalsecrets", "secretstores"], ["CREATE", "UPDATE", "DELETE"], [{
        expression = "request.userInfo.username in ${_cel_list(_users)} || request.userInfo.groups.exists(g, g in ${_cel_list(groups)})"
        message = "only the declared writers may create, update or delete ExternalSecrets and SecretStores on this cluster (ExternalSecretsOperator.admission.writers / writer_groups). Apply them through your env's apply identity"
    }])
}

# eso-store-pin: what a SecretStore may point at.
_store_pin_vap = lambda owner: str, sa: str, servers: [str], mounts: [str], audiences: [str], \
        gcp_projects: [str], gcp_sas: [str] -> [any] {
    _p = "object.spec.provider"
    _a = "${_p}.vault.auth"
    _only_jwt = "!has(${_a}.appRole) && !has(${_a}.cert) && !has(${_a}.gcp) && !has(${_a}.iam) && !has(${_a}.kubernetes) && !has(${_a}.ldap) && !has(${_a}.tokenSecretRef) && !has(${_a}.userPass)"
    _tok = "${_a}.jwt.kubernetesServiceAccountToken"
    _vault = "has(${_p}.vault) && ${_p}.vault.server in ${_cel_list(servers)} && has(${_a}) && has(${_a}.jwt) && ${_only_jwt} && ${_a}.jwt.path in ${_cel_list(mounts)} && !has(${_a}.jwt.secretRef) && has(${_tok}) && ${_tok}.serviceAccountRef.name == '${sa}' && (!has(${_tok}.serviceAccountRef.namespace) || ${_tok}.serviceAccountRef.namespace == object.metadata.namespace) && has(${_tok}.audiences) && size(${_tok}.audiences) == 1 && ${_tok}.audiences[0] in ${_cel_list(audiences)}"
    _g = "${_p}.gcpsm"
    _gcp = "has(${_g}) && ${_g}.projectID in ${_cel_list(gcp_projects)} && (!has(${_g}.auth) || (!has(${_g}.auth.secretRef) && !has(${_g}.auth.workloadIdentityFederation))) && (!has(${_g}.auth) || !has(${_g}.auth.workloadIdentity) || ${_g}.auth.workloadIdentity.serviceAccountRef.name in ${_cel_list([sa] + gcp_sas)})"
    _vap("eso-store-pin", owner, ["secretstores"], ["CREATE", "UPDATE"], [{
        expression = "${_spec_unchanged} || (${_vault}) || (${_gcp})"
        message = "a SecretStore here may only dial a declared server over JWT auth (a declared mount, audience and the one store service account), or a declared Google project over Workload Identity: any other provider would ship a service-account token or read access somewhere forge did not pin (ExternalSecretsOperator.admission.servers / jwt_mounts / audiences / gcp_projects)"
    }])
}

# eso-es-policy: what an ExternalSecret may ask for.
_es_policy_vap = lambda owner: str -> [any] {
    _t = "object.spec.target"
    _vap("eso-es-policy", owner, ["externalsecrets"], ["CREATE", "UPDATE"], [
        {
            expression = "${_spec_unchanged} || !has(object.spec.dataFrom) || size(object.spec.dataFrom) == 0"
            message = "an ExternalSecret may not use dataFrom: it copies every key a store path holds, so a Secret would carry whatever the path gains later. List each key under data"
        }
        {
            expression = "${_spec_unchanged} || !has(object.spec.secretStoreRef) || !has(object.spec.secretStoreRef.kind) || object.spec.secretStoreRef.kind == 'SecretStore'"
            message = "an ExternalSecret may reference only a namespaced SecretStore: ClusterSecretStore is not installed on this cluster"
        }
        {
            expression = "${_spec_unchanged} || (has(${_t}.creationPolicy) && ${_t}.creationPolicy in ['Orphan', 'Merge'])"
            message = "an ExternalSecret's target.creationPolicy must be Orphan or Merge: Owner deletes the Secret with the ExternalSecret, and None/CreateOrMerge are not part of the design"
        }
        {
            expression = "${_spec_unchanged} || (has(${_t}.deletionPolicy) && ${_t}.deletionPolicy == 'Retain')"
            message = "an ExternalSecret's target.deletionPolicy must be Retain: a store outage or a removed value must never delete a live Secret"
        }
        {
            expression = "${_spec_unchanged} || !has(${_t}.name) || ${_t}.name == object.metadata.name"
            message = "an ExternalSecret may write only the Secret named like itself (target.name unset or equal to metadata.name): a different name could overwrite another Secret in the namespace"
        }
        {
            expression = "${_spec_unchanged} || !has(${_t}.manifest)"
            message = "an ExternalSecret may not use a generic target (target.manifest): only Secrets are produced"
        }
        {
            expression = "${_spec_unchanged} || !has(object.spec.data) || !object.spec.data.exists(d, has(d.sourceRef))"
            message = "an ExternalSecret data entry may not name its own sourceRef: every entry reads from the ExternalSecret's SecretStore"
        }
        {
            expression = "${_spec_unchanged} || !has(object.spec.refreshInterval) || duration(object.spec.refreshInterval) >= duration('5m')"
            message = "an ExternalSecret's refreshInterval must be at least 5m: a faster loop is load on the store"
        }
    ])
}

# ---------------------------------------------------------------------------
# The chart
# ---------------------------------------------------------------------------

# The image reference split into the chart's repository and tag. The tag keeps
# its `@sha256:` digest, which is how the chart renders `repository:tag`.
_image_repository = lambda image: str -> str {
    image[:image.find(":", image.rfind("/"))]
}
_image_tag = lambda image: str -> str {
    image[image.find(":", image.rfind("/")) + 1:]
}

# deep_merge(a, b) — b over a, last wins: two maps merge key by key, anything
# else (a scalar, a list) is replaced by b's. KCL's own `|` refuses two
# different scalars on one path, which is exactly what an overlay overriding a
# default is, so the merge is spelled out.
_deep_merge = lambda a: any, b: any -> any {
    {
        k: (_deep_merge(a[k], b[k]) if (k in a and k in b and typeof(a[k]) == "dict" and typeof(b[k]) == "dict") else (b[k] if k in b else a[k]))
        for k in sorted({x: 0 for x in [y for y in a] + [z for z in b]})
    }
}

# A value that goes into a CEL string literal: no quote, backslash or control
# character (the same rule as schema.k _ESO_URL_RE).
_CEL_SAFE_RE = r"^[A-Za-z0-9][A-Za-z0-9._:@/+~%=?&#,-]{0,1000}$"

# admission_list(field, explicit, derived) — the explicit list when the author
# gave one, else the list derived from the render (lib/external_secret.k
# admission_defaults). A derived value is checked like an explicit one: it
# lands in a CEL literal.
_admission_list = lambda field: str, explicit: [str], derived: [str] -> [str] {
    _l = explicit if explicit else derived
    assert all x in _l { regex.match(x, _CEL_SAFE_RE) }, "ExternalSecretsOperator.admission.${field} derived ${[x for x in _l if not regex.match(x, _CEL_SAFE_RE)]}, which cannot be put in an admission policy. Set admission.${field} explicitly"
    _l
}

# eso_chart(e, owner, derived) — the forge.HelmChart (as a dict) for one
# declared ExternalSecretsOperator `e`. `owner` is the component-owner label
# value. `derived` is what the Bundle's own stores and OpenBao imply for the
# admission lists (lib/external_secret.k admission_defaults): an
# `admission` list left empty takes it, one that is set wins.
# `e` is the schema instance passed as `any` (this file is a sub-package of the
# module that declares the schema, so it cannot name it).
eso_chart = lambda e: any, owner: str, derived: any -> {str: any} {
    _ns = e.namespace
    _adm = e.admission
    _servers = _admission_list("servers", _adm.servers, derived.servers)
    _mounts = _admission_list("jwt_mounts", _adm.jwt_mounts, derived.jwt_mounts)
    _audiences = _admission_list("audiences", _adm.audiences, derived.audiences)
    _gcp = _admission_list("gcp_projects", _adm.gcp_projects, derived.gcp_projects)
    _gcp_sas = _admission_list("gcp_service_accounts", [], derived.gcp_service_accounts)
    _eso_user = "system:serviceaccount:${_ns}:${ESO_SERVICE_ACCOUNT}"
    _replicas = e.replicas
    _egress_to = [{ipBlock.cidr = c} for c in _adm.egress_cidrs]
    _posture = {
        "installCRDs": False
        "crds": {
            "createClusterExternalSecret": False
            "createClusterSecretStore": False
            "createClusterGenerator": False
            "createClusterPushSecret": False
            "createPushSecret": False
            "createSecretStore": True
            "conversion": {"enabled": False}
            "unsafeServeV1Beta1": False
        }
        "processSecretStore": True
        "processClusterStore": False
        "processClusterExternalSecret": False
        "processPushSecret": False
        "processClusterPushSecret": False
        "processClusterGenerator": False
        "webhook": {
            "create": False
            "tls": {"minVersion": "1.2", "ciphers": ESO_TLS_CIPHERS}
        }
        "certController": {"create": False}
        "rbac": {
            "serviceAccountTokenCreate": False
            "aggregateToView": False
            "aggregateToEdit": False
            "aggregateToAdmin": False
            "servicebindings": {"create": False}
        }
        "serviceAccount": {
            "create": True
            "name": ESO_SERVICE_ACCOUNT
            "automount": True
            "annotations": {"iam.gke.io/gcp-service-account": e.gcp_service_account} if e.gcp_service_account else {}
        }
        "image": {
            "repository": _image_repository(e.image)
            "tag": _image_tag(e.image)
            "pullPolicy": "IfNotPresent"
        }
        "replicaCount": _replicas
        "leaderElect": True
        "tls": {"minVersion": "1.2", "ciphers": ESO_TLS_CIPHERS}
        "extraArgs": {
            "enable-secrets-caching": "false"
            "enable-configmaps-caching": "false"
            "enable-managed-secrets-caching": "true"
            "enable-flood-gate": "true"
            "unsafe-allow-generic-targets": "false"
        }
        "resources": {
            "requests": {"cpu": "50m", "memory": "128Mi"}
            "limits": {"cpu": "500m", "memory": "512Mi"}
        }
        # Not _labels(): the chart already writes managed-by itself, and a
        # second key would be a duplicate in the rendered YAML.
        "commonLabels": {
            "${ESO_COMPONENT_LABEL}" = ESO_COMPONENT
            "${ESO_COMPONENT_OWNER_LABEL}" = owner
        }
        "podDisruptionBudget": {"enabled": _replicas > 1, "minAvailable": 1}
        "topologySpreadConstraints": [{
            "maxSkew": 1
            "topologyKey": "kubernetes.io/hostname"
            "whenUnsatisfiable": "ScheduleAnyway"
        }]
        # Off the kata / workspace pools: a customer workload's node is never the
        # operator's. NotIn also matches a node with no such label.
        "affinity": {
            "nodeAffinity": {
                "requiredDuringSchedulingIgnoredDuringExecution": {
                    "nodeSelectorTerms": [{
                        "matchExpressions": [{"key": "reliant.dev/runtime", "operator": "NotIn", "values": ["kata"]}]
                    }]
                }
            }
        }
        "serviceMonitor": {"enabled": False}
        "networkPolicy": {
            "enabled": True
            "ingress": [{
                "from": [{"namespaceSelector": {"matchExpressions": [{"key": "kubernetes.io/metadata.name", "operator": "In", "values": _adm.scrape_namespaces}]}}]
                "ports": [{"protocol": "TCP", "port": 8080}]
            }] if _adm.scrape_namespaces else []
            "egress": [
                {"ports": [{"protocol": "UDP", "port": 53}, {"protocol": "TCP", "port": 53}]}
                {
                    "to": [{"ipBlock": {"cidr": c}} for c in _adm.egress_cidrs]
                    "ports": [{"protocol": "TCP", "port": p} for p in _adm.egress_ports]
                }
            ]
        }
    } | ({"priorityClassName": e.priority_class} if e.priority_class else {})
    _admission = _writers_vap(owner, _eso_user, _adm.writers, _adm.writer_groups) \
        + _store_pin_vap(owner, _adm.store_service_account, _servers, _mounts, _audiences, _gcp, _gcp_sas) \
        + _es_policy_vap(owner)
    _merged = _deep_merge(_posture, e.values)
    # extraObjects are APPENDED, not replaced: adding one object never costs
    # the admission policies.
    _user_extra = e.values?.extraObjects or []
    {
        name = ESO_CHART_NAME
        oci = ESO_CHART_OCI
        version = e.version
        namespace = _ns
        crds = "external-secrets"
        cluster = e.cluster
        namespace_labels = _psa()
        values = _deep_merge(_merged, {"extraObjects": _admission + _user_extra})
    }
}
