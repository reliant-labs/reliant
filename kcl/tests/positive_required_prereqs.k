"""
Positive test — declared external PREREQUISITES (forge.ExternalSecret /
forge.DNSRecord on Bundle.required_secrets / required_dns) render through
the JSON contract (top-level `required_secrets` / `required_dns` buckets).

THE MODEL: a deploy depends on out-of-band facts forge does NOT create —
the cert-manager `cloudflare-api-token` Secret, per-host DNS A-records, the
load-bearing `*.workspaces` wildcard. Today those live only in KCL
docstrings, so render succeeds and ACME/DNS then hangs silently. Modeling
them here makes them a render-time checklist + a deploy preflight (a
declared-required-but-absent ExternalSecret BLOCKS) + a cross-secret
byte-match group.

A prerequisite NEVER renders into the k8s manifest stream — forge does not
create it; the declaration drives the checklist + preflight only.
"""

import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "prod-cluster"
    namespace = "control-plane-prod"
    platform = "amd64"
}

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = _target
    workloads = [
        fw.Workload {
            name = "api"
            image = "ghcr.io/reliant-labs/api"
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _target}
        }
    ]
    required_secrets = [
        # The motivating case: cert-manager's DNS-01 ClusterIssuer reads this
        # Secret (key api-token) in the cert-manager namespace — a DIFFERENT
        # namespace from the deploy's own.
        forge.ExternalSecret {
            name = "cloudflare-api-token"
            namespace = "cert-manager"
            keys = ["api-token"]
            reason = "cert-manager DNS-01 Cloudflare API token (writes _acme-challenge TXT)"
        }
        # A byte-match group: the same token wanted under a second ref. Both
        # members declare the SAME key set (enforced by the Bundle check).
        forge.ExternalSecret {
            name = "cloudflare-api-token"
            namespace = "control-plane-prod"
            keys = ["api-token"]
            value_group = "cf-token"
        }
        forge.ExternalSecret {
            name = "cf-token-mirror"
            namespace = "control-plane-prod"
            keys = ["api-token"]
            value_group = "cf-token"
        }
    ]
    required_dns = [
        # The load-bearing wildcard — its omission hangs both the DNS-01
        # wildcard cert and all workspace-proxy traffic. Target unknown at
        # author time (ephemeral Envoy LB IP), so omitted.
        forge.DNSRecord {
            host = "*.workspaces.reliantapi.com"
            reason = "DNS-01 wildcard cert + workspace-proxy traffic"
        }
        forge.DNSRecord {
            host = "api.reliantapi.com"
            type = "A"
            target = "203.0.113.10"
        }
    ]
}

output = forge.render(_bundle)

# ── required_secrets bucket ──────────────────────────────────────────
assert_req_secrets_present = "required_secrets" in output
assert_req_secrets_count = len(output.required_secrets) == 3

_cf = output.required_secrets[0]
assert_cf_name = _cf.name == "cloudflare-api-token"
assert_cf_ns = _cf.namespace == "cert-manager"
assert_cf_keys = _cf.keys == ["api-token"]
assert_cf_reason = "DNS-01" in _cf.reason
assert_cf_no_group = _cf.value_group == None

assert_group_member_group = output.required_secrets[1].value_group == "cf-token"
assert_group_member2_group = output.required_secrets[2].value_group == "cf-token"

# ── required_dns bucket ──────────────────────────────────────────────
assert_req_dns_present = "required_dns" in output
assert_req_dns_count = len(output.required_dns) == 2
assert_dns_wildcard_host = output.required_dns[0].host == "*.workspaces.reliantapi.com"
assert_dns_wildcard_type = output.required_dns[0].type == "A"
assert_dns_wildcard_no_target = output.required_dns[0].target == None
assert_dns_api_target = output.required_dns[1].target == "203.0.113.10"

# ── a prerequisite NEVER renders into the k8s manifest stream ─────────
# forge doesn't create these; the declaration drives the checklist +
# preflight only. No Secret object for the out-of-band Secret leaks in.
manifests = forge.render(_bundle).manifests
_leaked = [m for m in manifests if m.kind == "Secret" and m.metadata.name == "cloudflare-api-token"]
assert_no_prereq_secret_in_render = len(_leaked) == 0
