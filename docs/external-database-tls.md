# Authenticated TLS for external databases

An external database route can opt into `sslmode = "verify-full"` without
changing the legacy `require` (encryption without authentication) or `disable`
semantics. Forge does not provision certificates, a CA, or reader credentials.

```kcl
forge.ExternalDatabase {
    name = "ledger"
    service = "pooler"
    database = "ledger"
    username = "ledger_ro"
    password_secret = "ledger-reader"
    sslmode = "verify-full"
    ca_configmap = "pooler-trust" # alternatively: ca_secret
    ca_key = "ca.crt"
    # tls_server_name = "pooler.app-prod.svc" # derived when omitted
}
```

Exactly one of `ca_secret` and `ca_configmap`, plus an explicit `ca_key`, is
required. Trust is read from the route's resolved namespace. The selected key
must contain a nonempty certificate-only PEM bundle. ConfigMap `data` and
`binaryData` are supported, but the same key in both is refused. System trust
roots are not added. The server must present a certificate trusted by this
bundle and valid for the selected DNS SAN; there is no plaintext fallback.

`tls_server_name` defaults to `<service>.<namespace>.svc`, not the local tunnel
address. An explicit lowercase DNS name is accepted; IP addresses, wildcards,
ports and filesystem paths are not. All trust fields are refused with other
SSL modes. The CLI validates references and trust material before reading the
password. It reads current trust on each invocation; existing configurations
and pools retain their trust snapshot, so long-lived consumers must rebuild
both on CA rotation. Operators own certificate delivery, expiry and rotation.

## Recorded route contract

`release.ShapeExternalDatabase` adds comparable string fields `CASecret`,
`CAConfigMap`, `CAKey`, and `TLSServerName` with the snake-case JSON names above.
No certificate bytes, private keys, or local paths belong in recorded routes.
`Validate()` checks a complete route without accessing credentials. Use
`Shape.Canonical()` or `Shape.ExternalDatabase(name)` to resolve defaults;
verified routes record the derived DNS identity as well as every trust selector.
Authorization must compare that full resolved route **before** credentials,
network access, or a cached response. Selector equality does not detect changed
CA bytes and is not a credential/pool cache invalidation strategy.

Readers advertise `release.BundleFeatureExternalDatabaseTLS`
(`shape-external-database-tls`) through `BundleDocFeatures()`. For a reader that
understands external databases but not authenticated TLS, `LimitShape` omits
the entire verified route, never just its trust fields and never by downgrading
it to `require`. Legacy routes retain their original defaults and canonical
encoding. Omitting a route only withholds database browsing; it does not alter
the application's own connection or provision anything.
