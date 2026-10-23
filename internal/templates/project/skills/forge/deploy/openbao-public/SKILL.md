---
name: deploy/openbao-public
description: Publishing forge.OpenBao on a Gateway so clusters elsewhere can read from it — the listener and certificate forge adds, the three paths the route allows (everything else is a 404), the re-encrypt and what verifies it, the limits and their scope, how X-Forwarded-For reaches the audit log, and each refusal with its escape. Use when you declare OpenBao.public, debug a remote SecretStore that is NotReady or a 404/403/503 on the public host, or review what the internet can reach.
---

# Publishing OpenBao: `public`

```python
openbao = forge.OpenBao {
    public = forge.OpenBaoPublic {host = "secrets.example.com", gateway = "public"}
}
```

`gateway` names a `forge.Gateway` on the cluster OpenBao runs on. Nothing
about the store changes for in-cluster callers.

## What forge adds

| Object | Where | What it does |
|---|---|---|
| HTTPS listener `<openbao>-public` | the Gateway | serves `host`; its own Certificate and Secret `<openbao>-public-tls` from the Gateway's issuer (a shared certificate would be re-issued when a host joins it, and Let's Encrypt allows five a week) |
| HTTPRoute `<openbao>-public` | the Gateway's namespace | **the allowlist** |
| BackendTLSPolicy | OpenBao's namespace | Envoy terminates TLS and re-encrypts, verifying OpenBao |
| BackendTrafficPolicy | the route | body cap (16Ki), total and per-source request limits |
| ClientTrafficPolicy | the listener | connection limit, request timeout, no client `X-Forwarded-For`, escaped slashes refused |
| ReferenceGrant | OpenBao's namespace | only when the Gateway is in another namespace; names the one Service |
| listener `public.port` (8210) on OpenBao | the pods | the only listener that trusts `X-Forwarded-For` |
| NetworkPolicy rule | OpenBao | opens `port` to the proxy pods of that one Gateway |

## The allowlist

One rule, four matches, nothing else routed (any method, any other path is a
404 from Envoy, so `/v1/sys/*`, metadata, listing, writes and the UI do not
exist from outside):

| Method | Path | Why |
|---|---|---|
| `PUT`, `POST` | `/v1/auth/jwt-<cluster>/login` | the service-account login (ESO sends `PUT`) |
| `GET` | `/v1/auth/token/lookup-self` | ESO calls it to validate a SecretStore and to decide whether to log in again; it returns only the caller's own token |
| `GET` | `/v1/<kv>/data/tenants/<org>/<env>/<NAME>` | one value; the segment classes are `pkg/secretaccess`'s, so the edge never routes a path no policy could grant |

`kubectl get httproute <openbao>-public -o yaml` is the list. A pinned read
(`?version=3`) passes: the query string is not part of the match.

Authorisation is still OpenBao's: a routed request without a token, or for a
name the role does not grant, is a 403 from OpenBao. The edge does not decide
who may read what.

## Verification of OpenBao (the re-encrypt)

The BackendTLSPolicy pins the CA ConfigMap `tls.ca_configmap` (the one the
converge Job publishes) and the Service's own name `<openbao>.<ns>.svc`, which
is also the SNI. The public host is **not** in OpenBao's certificate and need
not be. A wrong CA or name gives a **503 from Envoy**; OpenBao is not reached.
If every request is a 500, the CA ConfigMap is missing from OpenBao's
namespace (`kubectl get btlsp -n <openbao ns> -o yaml` says `InvalidCACertificateRef`).

## Limits

Envoy's local limiter, so **per Envoy replica**: `requests_per_second` (all
sources), `per_source_requests_per_second` (one address, IPv4 and IPv6 each),
`max_connections` (the listener) and `request_timeout`. They are flood guards,
not quotas. The total is a bucket that matches every request: a "default"
bucket beside a rule that matches every address is never consumed, so it would
cap nothing. A source that exhausts its own bucket is refused before it draws
on the total.

## X-Forwarded-For and the audit log

Envoy removes any `X-Forwarded-For` the client sent and appends the address of
the peer it sees. OpenBao trusts the header on `port` only, from
`trusted_proxy_cidrs`, and refuses (400) a peer outside them and a request with
no header: **both are set explicitly because OpenBao 2.6.2's documented
"default true" is false in code.** The audit log's `remote_address` is the
client. Behind another proxy (a Google load balancer in front of Envoy) the
address Envoy sees is that proxy's; put the hop in front of Envoy and count it
(`ClientTrafficPolicy` `clientIPDetection`), which forge does not do for you.

## Refusals and their escapes

| Refusal | Escape |
|---|---|
| the Gateway lands on another cluster | `forge.Gateway.cluster`, or run OpenBao where the Gateway is |
| no certificate for the host | `OpenBaoPublic.tls`, a `tls` on the Gateway, or an HTTPS listener for the host on the Gateway (used as it is) |
| `port` is a store listener | pick a free port |
| empty or invalid `trusted_proxy_cidrs` | the CIDRs the proxy pods get addresses from |
| `request_timeout` | whole numbers with h, m, s or ms (`30s`, `1m30s`): `1.5s` is refused by the API server |
| a limit of zero | unpublish instead of closing the endpoint |

DNS is yours: an A record for `host` to the Gateway's address; list it in
`Bundle.required_dns` so the deploy checklist shows it.

## Not run by forge's tests

Envoy Gateway's translation was checked with `egctl` from the v1.9.2 tag, and
the resulting upstream TLS and route table were run in Envoy against a real
OpenBao. cert-manager issuing the listener's certificate, a load balancer
programmed from the Gateway, NetworkPolicy enforcement by your CNI and the API
server admitting the CRs are for a real cluster: `forge env status` after the
first deploy.
