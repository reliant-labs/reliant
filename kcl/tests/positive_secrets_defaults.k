"""
Positive test — forge.OpenBao {} runs with nothing declared: Raft x3 (N1), TLS
listener, static seal with a generated 32-byte hex key, audit on, a
service-account writer (N2), exact-path-only consumers. The render lowers it to an
OpenBao record the Go renderer expands (pkg/deploy/openbao).
"""

# kcl-args: env=dev

import forge

_bundle = forge.Bundle {
    project = "fixture"
    cluster_target = forge.ClusterTarget {cluster = "k3d-fixture", namespace = "fixture"}
    openbao = forge.OpenBao {}
    bootstrap_source = forge.GCPSecretManager {project = "acme-prod"}
}

output = forge.render(_bundle)
_bao = _bundle.openbao

assert_storage_is_raft = _bao.storage.type == "raft" and _bao.replicas == 3
assert_tls_by_default = len(_bao.listeners) == 1 and _bao.listeners[0].tls and _bao.listeners[0].port == 8200
assert_seal = _bao.seal.type == "static" and _bao.seal.key_id == "dev-1" and _bao.seal.key.name == "openbao-unseal-dev"
assert_seal_key_generated = _bao.seal.key.generate.bytes == 32 and _bao.seal.key.generate.encoding == "hex"
assert_recovery_escrow = _bao.recovery.escrow.name == "openbao-recovery-dev" and not _bao.recovery.escrow.generate
assert_sa_writer = _bao.writer.type == "service_account" and _bao.writer.service_account == "forge-secret-writer"
assert_audit_on = _bao.audit.stdout and _bao.audit.file
assert_kv = _bao.kv.mount == "secret" and _bao.kv.max_versions == 10
assert_image_pinned = "@sha256:" in _bao.image
assert_bootstrap_source = _bundle.bootstrap_source.type == "gcpsm"
