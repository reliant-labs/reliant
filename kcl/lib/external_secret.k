"""
Forge KCL — store-backed ExternalSecrets rendered as External Secrets Operator
objects.

A `forge.ExternalSecret` with a `source = forge.StoreSource {...}` is a Secret
that ESO syncs from a secret store into the namespace its workloads already
read. For every (cluster, namespace) that holds one, this renders:

  * a ServiceAccount `reliant-secrets`: the identity whose projected token
    logs in to the store (OpenBao/hosted stores only);
  * a Role + RoleBinding letting ESO mint a token for THAT service account and
    no other (`resourceNames`). ESO runs with `rbac.serviceAccountTokenCreate:
    false`, so this is the only token-minting grant it has;
  * one `SecretStore` per store the namespace reads, derived from the store
    kind: the `vault` provider with JWT auth for HostedStore and OpenBaoStore,
    `gcpsm` for GCPSecretManagerStore;
  * one `ExternalSecret` per Secret: `data` (one remoteRef per store name, with
    the KV version when pinned), `target.template` for derived keys, the
    refresh interval, and creationPolicy Orphan|Merge with deletionPolicy
    Retain, so a store outage or a Flux prune never removes a synced Secret.
    A `shadow` source syncs to `<name>-eso-shadow` instead.

THE LOGIN CONTRACT. A SecretStore logs in as the OpenBao JWT role that
`pkg/secretaccess` derives for its (org, env, cluster, namespace), on the mount
`jwt-<cluster id>`, with a token whose audience is `<store>/c/<cluster id>`.
`_role_name` re-derives `secretaccess.Scope.PolicyName` in KCL; a Go test pins
the two together, so a change to one fails until the other follows.

Only namespaced `SecretStore`s are rendered: ESO runs with the cluster kinds
off (and their CRDs absent).

KCL package-identity note: lambda params are typed `any` so the parent-package
schemas are accepted as duck-typed inputs, as in lib/gateway.k.
"""

import crypto
import regex
import .openbao as openbao_lib

# The ServiceAccount whose token logs in to the store, in every namespace that
# holds a SecretStore. It is the subject secretaccess pins each role to
# (secretaccess.DefaultServiceAccount).
STORE_SERVICE_ACCOUNT: str = "reliant-secrets"

# The Role/RoleBinding that lets ESO mint that service account's token.
TOKEN_ROLE: str = "reliant-secrets-token"

# ESO's controller service account and the namespace it runs in when the
# Bundle does not declare the install itself (another env owns it). The chart's
# default fullname, which forge.ExternalSecretsOperator keeps.
ESO_SERVICE_ACCOUNT: str = "external-secrets"
ESO_DEFAULT_NAMESPACE: str = "external-secrets"

# Reliant's public secrets endpoint: the store a HostedStore reads from unless
# `server` says otherwise, and the base of the audience its roles require.
HOSTED_SECRETS_ENDPOINT: str = "https://secrets.reliantapi.com"

# The key in a CA ConfigMap that holds the PEM bundle.
CA_KEY: str = "ca.crt"

# The key under which every stored value lives (secret/.../NAME = {"value": ...}).
VALUE_PROPERTY: str = "value"

_CLUSTER_ID_RE: str = r"^[A-Za-z0-9][A-Za-z0-9_-]*$"
_SEGMENT_RE: str = r"^[A-Za-z0-9][A-Za-z0-9._-]*$"
_UUID_RE: str = r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"
_SHORT_RE: str = r"^[a-z0-9]{1,8}$"

# Template functions ESO's engine does not have (sprig's non-hermetic ones are
# removed) or whose output changes on every sync, which would rewrite the
# Secret each refresh and roll its consumers.
_BANNED_TEMPLATE_FUNCS: [str] = [
    "env", "expandenv", "getHostByName",
    "now", "ago", "date", "dateInZone", "date_in_zone", "dateModify", "date_modify", "mustDateModify", "must_date_modify", "htmlDate", "htmlDateInZone", "unixEpoch",
    "randAlphaNum", "randAlpha", "randAscii", "randNumeric", "randBytes", "randInt", "rand", "uuidv4", "shuffle",
    "genPrivateKey", "genCA", "genCAWithKey", "genSelfSignedCert", "genSelfSignedCertWithKey", "genSignedCert", "genSignedCertWithKey", "buildCustomCert",
    "bcrypt", "htpasswd", "derivePassword",
]

# --- Derivations ------------------------------------------------------------

# A short readable label for an identifier: a UUID becomes its first group, a
# short lowercase alphanumeric stays, anything else becomes eight hex digits of
# its digest. (secretaccess.label)
_label = lambda id: str -> str {
    id[:8] if regex.match(id, _UUID_RE) else (id if regex.match(id, _SHORT_RE) else crypto.sha256(id)[:8])
}

# The OpenBao role (and policy) name of one namespace of one cluster:
# env-<env>-<cluster>-<hash of the whole tuple>. (secretaccess.Scope.PolicyName)
_role_name = lambda org: str, env: str, cluster_id: str, namespace: str -> str {
    "env-${_label(env)}-${_label(cluster_id)}-${crypto.sha256(org + '\x00' + env + '\x00' + cluster_id + '\x00' + namespace)[:8]}"
}

_eso_context = lambda e: any -> str {
    e.cluster.context if typeof(e.cluster) == "Cluster" else e.cluster.cluster
}

# The kubectl context a source lands on: its own `cluster`, else the Bundle's
# primary cluster.
_context = lambda src: any, pt: any -> str {
    src.cluster if src.cluster else (pt.cluster if pt else "")
}

# The id a cluster is registered under in the store: the name `forge cluster
# connect` recorded, else the context. Same precedence as pkg/release
# (ShapeStoreSecret.ConnectedCluster, then Cluster).
_cluster_id = lambda b: any, pt: any, ctx: str -> str {
    _from_map = b.connected_clusters[ctx] if ctx in b.connected_clusters else ""
    _from_pt = pt.connected_cluster if pt and pt.cluster == ctx and pt.connected_cluster else ""
    _from_wl = [w.runtime.target.connected_cluster for w in b.workloads if w.runtime and w.runtime.type == "cluster" and w.runtime.target.cluster == ctx and w.runtime.target.connected_cluster]
    _from_eso = [e.cluster.connected_cluster for e in b.external_secrets_operators if typeof(e.cluster) == "ClusterTarget" and e.cluster.cluster == ctx and e.cluster.connected_cluster]
    _from_cps = [c.cluster.connected_cluster for c in b.control_plane_secrets if c.cluster.cluster == ctx and c.cluster.connected_cluster]
    _from_map or _from_pt or (_from_wl[0] if _from_wl else "") or (_from_eso[0] if _from_eso else "") or (_from_cps[0] if _from_cps else "") or ctx
}

# The ESO install a context's SecretStores are served by, declared in this
# Bundle or not.
_eso_for = lambda b: any, ctx: str -> [any] {
    [e for e in b.external_secrets_operators if _eso_context(e) == ctx]
}

_eso_namespace = lambda b: any, ctx: str -> str {
    _e = _eso_for(b, ctx)
    _e[0].namespace if _e else ESO_DEFAULT_NAMESPACE
}

# The context OpenBao runs in: its own runtime, else the primary cluster. ""
# when the Bundle declares no OpenBao (assert messages are evaluated eagerly,
# so every helper they call must tolerate an absent component).
_openbao_context = lambda b: any, pt: any -> str {
    "" if not b.openbao else (b.openbao.runtime.target.cluster if b.openbao.runtime else (pt.cluster if pt else ""))
}

_openbao_name = lambda b: any -> str {
    b.openbao.name if b.openbao else "openbao"
}

# The in-cluster URL of the component's first TLS listener.
_openbao_service_url = lambda ob: any -> str {
    "https://${ob.name}.${ob.namespace}.svc:${[l.port for l in ob.listeners if l.tls][0]}"
}

# The base every cluster's audience hangs off: where the store is reached from
# outside, else its Service.
_openbao_audience_base = lambda ob: any -> str {
    "https://${ob.public.host}" if ob.public else _openbao_service_url(ob)
}

# --- forge.ControlPlaneSecret delivered through the store --------------------
#
# A ControlPlaneSecret with `delivery = "store"` is a Secret the control plane
# WRITES TO THE STORE and ESO delivers, as a floating ExternalSecret: no
# `version`, because the control plane renews the value in place and the
# consumer re-reads the file the kubelet refreshes. It is rendered by the same
# code as a `required_secrets` store source (`manifests` below), from the
# StoreSource-shaped record `cps_source` builds, so the SecretStore, the login
# role and every refusal are one implementation.
#
# THE CONTRACT with the control plane: the store name each kind is written
# under. forge.ControlPlaneSecret kind telemetry_token writes ONE name,
# `RELIANT_OTEL_HEADERS` = `authorization=Bearer <jwt>` (the OTEL headers
# format); the Secret's two keys are the value as-is and the bare token, derived
# by the template below, so they can never disagree about the current token.
# pkg/release.ControlPlaneSecretStoreName is the Go spelling, pinned to this by
# a render test.
TELEMETRY_TOKEN_STORE_NAME: str = "RELIANT_OTEL_HEADERS"
_TELEMETRY_HEADERS_KEY: str = "OTEL_EXPORTER_OTLP_HEADERS"
_TELEMETRY_BEARER_PREFIX: str = "authorization=Bearer "

# The StoreSource-shaped record of one store-delivered ControlPlaneSecret.
cps_source = lambda s: any -> any {
    {
        store = s.store
        data = {"${_TELEMETRY_HEADERS_KEY}" = TELEMETRY_TOKEN_STORE_NAME}
        templates = {token = "{{ .${_TELEMETRY_HEADERS_KEY} | trimPrefix \"${_TELEMETRY_BEARER_PREFIX}\" }}"}
        generate = {}
        pin = {}
        immutable = []
        refresh = s.refresh
        creation_policy = "Orphan"
        shadow = False
        cluster = s.cluster.cluster
    }
}

# The ExternalSecret-shaped records of the Bundle's store-delivered
# ControlPlaneSecrets. Never emitted as `required_secrets`: the deploy must not
# wait for a Secret the control plane writes after it.
cps_secrets = lambda b: any -> [any] {
    [{
        name = s.name
        namespace = s.namespace or s.cluster.namespace
        source = cps_source(s)
    } for s in b.control_plane_secrets if s.delivery == "store"]
}

# --- Stores -----------------------------------------------------------------

# One resolved store for a (cluster, namespace): its SecretStore name, spec and
# whether it logs in through the namespace's service account.
_resolve_store = lambda b: any, pt: any, src: any, ctx: str, ns: str -> any {
    _store = src.store
    _id = _cluster_id(b, pt, ctx)
    _ok_id = regex.match(_id, _CLUSTER_ID_RE)
    _hosted = _store.type == "hosted"
    _ob = _store.type == "openbao"
    _org = (_store.org if _hosted else b.project)
    _env = (_store.env_id if _hosted else b.env)
    _ob_ctx = _openbao_context(b, pt) if _ob else ""
    _in_cluster = _ob and _ob_ctx == ctx
    _server = (_store.server or HOSTED_SECRETS_ENDPOINT) if _hosted else \
        ((_store.server or (_openbao_service_url(b.openbao) if _in_cluster else ("https://${b.openbao.public.host}" if b.openbao.public else ""))) if _ob else "")
    _ca = _store.ca_configmap or ((b.openbao.tls.ca_configmap or "${b.openbao.name}-ca") if _in_cluster and not _store.server else "") if (_hosted or _ob) else ""
    _aud_base = HOSTED_SECRETS_ENDPOINT if _hosted else (_openbao_audience_base(b.openbao) if _ob else "")
    _mount = "secret" if _hosted else (b.openbao.kv.mount if _ob else "")
    _is_vault = _hosted or _ob
    _ = {
        "vault": _is_vault
        "id": _id
        "ok_id": _ok_id
        "org": _org
        "env": _env
        "server": _server
        "ca": _ca
        "audience": "${_aud_base}/c/${_id}"
        "mount": _mount
        "name": "reliant-hosted" if _hosted else ("reliant-openbao" if _ob else "reliant-gsm-${_store.project}")
        "type": _store.type
        "project": _store.project if _store.type == "gcpsm" else ""
    }
    _
}

# The ESO `SecretStore` provider block for a resolved store.
_provider = lambda st: any, role: str -> any {
    {
        vault = {
            server = st.server
            path = st.mount
            version = "v2"
            auth = {
                jwt = {
                    path = "jwt-${st.id}"
                    role = role
                    kubernetesServiceAccountToken = {
                        serviceAccountRef = {name = STORE_SERVICE_ACCOUNT}
                        audiences = [st.audience]
                    }
                }
            }
            if st.ca:
                caProvider = {type = "ConfigMap", name = st.ca, key = CA_KEY}
        }
    } if st.vault else {
        gcpsm = {projectID = st.project}
    }
}

# --- Refusals ---------------------------------------------------------------

# The banned template functions a template calls. String literals are blanked
# first so `printf "%s env"` names nothing; `.env` (a data key) is not a call.
_BANNED_RE: str = r"(?:^|[^.\w$])(" + "|".join(_BANNED_TEMPLATE_FUNCS) + r")(?:$|[^\w])"

_template_funcs = lambda t: str -> [str] {
    [m for act in regex.findall(t, r"\{\{.*?\}\}") for m in regex.findall(regex.replace(act, r'"[^"]*"', '""'), _BANNED_RE)]
}

_all_templates = lambda src: any -> str {
    " ".join([v for _k, v in src.templates])
}

_eso_without_gsa = lambda b: any, ctx: str -> [str] {
    [e.namespace for e in _eso_for(b, ctx) if not e.gcp_service_account]
}

_check_source = lambda b: any, pt: any, s: any -> bool {
    _src = s.source
    _ctx = _context(_src, pt)
    _st = _resolve_store(b, pt, _src, _ctx, s.namespace)
    _where = "ExternalSecret '${s.namespace}/${s.name}'"
    _funcs = _template_funcs(_all_templates(_src))
    _path_ok = regex.match(_st.org, _SEGMENT_RE) and regex.match(_st.env, _SEGMENT_RE)
    assert _ctx, "${_where} reads from a store but lands on no cluster: the Bundle has no primary cluster (cluster_target or a cluster-bound workload). Set `source.cluster` to the kubectl context of the cluster it lands on, or declare a cluster_target"
    assert not _st.vault or _st.ok_id, "${_where} lands on cluster '${_st.id}', which is not usable as a store mount id (letters, digits, '_' and '-'). Register the cluster under a plain name: `connected_cluster` on its forge.ClusterTarget, or Bundle.connected_clusters = {\"${_ctx}\" = \"<name>\"}"
    assert _st.type != "hosted" or _path_ok, "${_where} reads from a forge.HostedStore with no `org` / `env_id`. They are the ids the control plane assigned this env's organization and environment (they address its values and name the JWT role); set them: forge.HostedStore {org = \"<org id>\", env_id = \"<env id>\"}"
    assert _st.type != "openbao" or _path_ok, "${_where} reads from a forge.OpenBaoStore, whose values live under the Bundle's project and env (secret/data/tenants/<project>/<env>/<NAME>). Bundle.project '${_st.org}' / Bundle.env '${_st.env}' are not plain path segments: set Bundle.env (or bind -D env=<name>)"
    assert _st.type != "openbao" or _st.server, "${_where} reads from forge.OpenBao '${_openbao_name(b)}' (${_openbao_context(b, pt)}) from cluster '${_ctx}', and the component is not published there. Declare `openbao.public = forge.OpenBaoPublic {...}` so other clusters can reach it, or set `server` (and `ca_configmap`) on the forge.OpenBaoStore"
    assert _st.type != "gcpsm" or len(_eso_without_gsa(b, _ctx)) == 0, "${_where} reads from a forge.GCPSecretManagerStore, and this Bundle's External Secrets Operator on '${_ctx}' has no `gcp_service_account`. The store authenticates as ESO's own identity over Workload Identity; set forge.ExternalSecretsOperator.gcp_service_account to the Google service account ESO federates to"
    assert len(_funcs) == 0, "${_where} templates call ${_funcs}, which ESO's template engine does not have or which changes on every sync (the Secret would be rewritten each refresh and its consumers rolled). Derive the value from the store's data with the deterministic functions (urlquery, b64enc, sha256sum, printf, ...), or generate it once with `source.generate`"
    True
}

# --- Admission defaults -----------------------------------------------------

# What the cluster `ctx`'s External Secrets Operator admits when its
# `admission` lists are left empty: exactly the SecretStores this Bundle
# renders there, plus the in-cluster store when forge.OpenBao runs on `ctx`.
# An empty list is not a wildcard, so without this the three policies would
# refuse every store the Bundle itself renders. Sorted and de-duplicated.
#
#   servers      each store's URL (the hosted endpoint, the component's Service)
#   jwt_mounts   the JWT auth mount each store logs in on (jwt-<cluster id>)
#   audiences    the one audience each store requests
#   gcp_projects the Google project of each gcpsm store
#   gcp_service_accounts the extra service accounts a gcpsm store may use: the
#                component's bootstrap reader
#
# The component's own server/mount/audience are admitted even when no
# ExternalSecret reads from it yet, so adding the first one needs no policy
# edit. An OpenBaoStore with no component declared is skipped here; the
# source check refuses it with the real reason.
admission_defaults = lambda b: any, pt: any, ctx: str -> any {
    _recs = [
        _resolve_store(b, pt, s.source, _context(s.source, pt), s.namespace)
        for s in b.required_secrets if s.source and _context(s.source, pt) == ctx and (s.source.store.type != "openbao" or b.openbao)
    ]
    _vault = [st for st in _recs if st.vault]
    _ob = True if b.openbao and _openbao_context(b, pt) == ctx else False
    _id = _cluster_id(b, pt, ctx)
    # The component's own bootstrap store (lib/openbao.k): a SecretStore over
    # Secret Manager in the bootstrap project, which the policy must admit or the
    # seal key never arrives.
    _boot = [b.bootstrap_source.project] if (_ob and b.bootstrap_source and b.bootstrap_source.type == "gcpsm") else []
    _boot_sa = [openbao_lib.reader_service_account(b.openbao, b.bootstrap_source)] if _boot else []
    {
        "servers": sorted({x: 0 for x in [st.server for st in _vault if st.server] + ([_openbao_service_url(b.openbao)] if _ob else [])})
        "jwt_mounts": sorted({x: 0 for x in ["jwt-${st.id}" for st in _vault] + (["jwt-${_id}"] if _ob else [])})
        "audiences": sorted({x: 0 for x in [st.audience for st in _vault] + (["${_openbao_audience_base(b.openbao)}/c/${_id}"] if _ob else [])})
        "gcp_projects": sorted({x: 0 for x in [st.project for st in _recs if st.type == "gcpsm"] + _boot})
        "gcp_service_accounts": _boot_sa
    }
}

# --- The render --------------------------------------------------------------

# `forge.dev/secret-stage: secrets` is what lets the applier order these ahead
# of the workloads that read them (pkg/release.ClassifySecretStage): the login
# identity is not an ESO kind, and an ExternalSecret that cannot log in would
# never sync. Stamped on every object this module renders.
_labels = lambda b: any, ctx: str -> {str: str} {
    {
        "app.kubernetes.io/managed-by" = "forge"
        "app.kubernetes.io/part-of" = b.project if b.project else "forge"
        "forge.dev/cluster" = ctx
        "forge.dev/secret-stage" = "secrets"
    }
}

_service_account = lambda b: any, ctx: str, ns: str -> any {
    {
        apiVersion = "v1"
        kind = "ServiceAccount"
        metadata = {name = STORE_SERVICE_ACCOUNT, namespace = ns, labels = _labels(b, ctx)}
        automountServiceAccountToken = False
    }
}

_token_role = lambda b: any, ctx: str, ns: str -> [any] {
    [
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "Role"
            metadata = {name = TOKEN_ROLE, namespace = ns, labels = _labels(b, ctx)}
            rules = [{apiGroups = [""], resources = ["serviceaccounts/token"], resourceNames = [STORE_SERVICE_ACCOUNT], verbs = ["create"]}]
        }
        {
            apiVersion = "rbac.authorization.k8s.io/v1"
            kind = "RoleBinding"
            metadata = {name = TOKEN_ROLE, namespace = ns, labels = _labels(b, ctx)}
            roleRef = {apiGroup = "rbac.authorization.k8s.io", kind = "Role", name = TOKEN_ROLE}
            subjects = [{kind = "ServiceAccount", name = ESO_SERVICE_ACCOUNT, namespace = _eso_namespace(b, ctx)}]
        }
    ]
}

_secret_store = lambda b: any, ctx: str, ns: str, st: any -> any {
    {
        apiVersion = "external-secrets.io/v1"
        kind = "SecretStore"
        metadata = {name = st.name, namespace = ns, labels = _labels(b, ctx)}
        spec = {provider = _provider(st, _role_name(st.org, st.env, st.id, ns) if st.vault else "")}
    }
}

# The remoteRef key of a store name: the path under the KV mount.
_remote_key = lambda st: any, name: str -> str {
    name if st.type == "gcpsm" else "tenants/${st.org}/${st.env}/${name}"
}

# What a deploy does about an ExternalSecret (pkg/release.AnnotationSecretSync):
# a shadow copy gates nothing (no workload reads it), and a store-delivered
# ControlPlaneSecret is written AFTER the deploy by the control plane, so a
# deploy that waited for it would wait for a value that cannot exist yet.
_sync_annotation = lambda src: any, deferred: bool -> {str: str} {
    {"forge.dev/secret-sync" = "shadow"} if src.shadow else ({"forge.dev/secret-sync" = "deferred"} if deferred else {})
}

_external_secret = lambda b: any, ctx: str, s: any, st: any, deferred: bool -> any {
    _src = s.source
    _name = s.name + "-eso-shadow" if _src.shadow else s.name
    _tpl = _src.templates
    _sync = _sync_annotation(_src, deferred)
    # The template block is needed for derived keys, and also for a Secret whose
    # type or labels differ from ESO's default (a CloudNativePG basic-auth
    # password). A source that sets none renders exactly as before.
    _typed = _src.secret_type != "Opaque"
    {
        apiVersion = "external-secrets.io/v1"
        kind = "ExternalSecret"
        metadata = {
            name = _name
            namespace = s.namespace
            labels = _labels(b, ctx)
            if _sync:
                annotations = _sync
        }
        spec = {
            refreshInterval = _src.refresh
            secretStoreRef = {name = st.name, kind = "SecretStore"}
            target = {
                name = _name
                creationPolicy = "Orphan" if _src.shadow else _src.creation_policy
                deletionPolicy = "Retain"
                if _tpl or _typed or _src.labels:
                    template = {
                        engineVersion = "v2"
                        mergePolicy = "Merge"
                        if _typed:
                            type = _src.secret_type
                        if _src.labels:
                            metadata = {labels = {k: _src.labels[k] for k in sorted(_src.labels)}}
                        if _tpl:
                            data = {k: _tpl[k] for k in sorted(_tpl)}
                    }
            }
            data = [{
                secretKey = k
                remoteRef = {
                    key = _remote_key(st, _src.data[k])
                    if st.vault:
                        property = VALUE_PROPERTY
                    if _src.data[k] in _src.pin:
                        version = str(_src.pin[_src.data[k]])
                }
            } for k in sorted(_src.data)]
        }
    }
}

# The SecretStore name -> spec fingerprint of one record, to catch two sources
# in one namespace that name one store kind but configure it differently.
_fingerprint = lambda st: any -> str {
    "${st.name}|${st.server}|${st.ca}|${st.org}|${st.env}|${st.id}|${st.audience}|${st.mount}"
}

# The objects of one (cluster, namespace): the login identity when a vault
# store is in play, one SecretStore per store, one ExternalSecret per Secret.
_group_objects = lambda b: any, recs: [any] -> [any] {
    _ctx = recs[0].ctx
    _ns = recs[0].ns
    _stores = [r.store for i, r in recs if r.store.name not in [q.store.name for j, q in recs if j < i]]
    _vault = [st for st in _stores if st.vault]
    (([_service_account(b, _ctx, _ns)] + _token_role(b, _ctx, _ns)) if _vault else []) \
        + [_secret_store(b, _ctx, _ns, st) for st in _stores] \
        + [_external_secret(b, _ctx, r.secret, r.store, r.deferred) for r in recs]
}

# Every ESO object the Bundle's store-backed ExternalSecrets need, ordered by
# (cluster, namespace). Empty, and so a no-op for every other Bundle, when no
# ExternalSecret has a `source`.
manifests = lambda b: any, pt: any -> [any] {
    _cps = cps_secrets(b)
    _sources = [s for s in b.required_secrets if s.source] + _cps
    _checked = [_check_source(b, pt, s) for s in _sources]
    _recs = [{
        "ctx": _context(s.source, pt)
        "ns": s.namespace
        "group": "${_context(s.source, pt)}/${s.namespace}"
        "secret": s
        "store": _resolve_store(b, pt, s.source, _context(s.source, pt), s.namespace)
        "deferred": s in _cps
    } for s in _sources]
    _groups = sorted({r.group: 0 for r in _recs})
    _clash = [r.group + "/" + r.store.name for r in _recs for q in _recs if r.group == q.group and r.store.name == q.store.name and _fingerprint(r.store) != _fingerprint(q.store)]
    _clashes = sorted({c: 0 for c in _clash})
    assert len(_clashes) == 0, "Bundle.required_secrets: ExternalSecrets in one namespace read from two differently configured stores of the same kind (${_clashes}). A namespace has one SecretStore per kind; give the sources the same `server` / `ca_configmap` / `org` / `env_id`, or move one to another namespace"
    [m for g in _groups for m in _group_objects(b, [r for r in _recs if r.group == g])]
}
