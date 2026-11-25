---
name: deploy/domains
description: Serving your own hostname — the `forge domain` commands for hosted sites and ports, why a hosted domain is a control-plane resource rather than spec, `Port.domains` on a cluster you operate, reading a bound domain's state, and restricting a port to source addresses with `allowedSourceCIDRs`.
---

# Custom domains

Every hosted site and exposed hosted port already answers on a hostname the
platform allocates — you never declare that one. To ALSO serve your own, use
the `forge domain` commands. **A hosted domain is NOT spec**: a hosted
frontend or port that carries `domains` is refused at render.

```
forge domain add hounders.club --env prod                # prints the DNS to set
forge domain bind hounders.club --env prod --target web
forge domain bind www.hounders.club --env prod --redirect-to hounders.club
```

A `--redirect-to` binding answers every path with a permanent redirect to
the same path and query on the other host: 301 for GET and HEAD, 308 for
anything else. Point the www name at the same records as its apex (the
static-site edge serves redirects too). A redirect of one PATH (`/docs` to
another site) is not a binding: it is the site's own `_redirects` file
(`deploy/static-site`). `--hsts-include-subdomains` on a bind
adds `includeSubDomains` to that hostname's `Strict-Transport-Security`; it is
off by default because it breaks every subdomain still on plain HTTP for as
long as a browser remembers it (a year), and a rebind without it turns it off
(the bind says so). `ls` (HSTS column), `show` and `--json`
(`binding.hstsIncludeSubdomains`) report the current setting.

`ls`, `show`, `verify` (check DNS now), `unbind` (stop serving, keep the
verification) and `rm` (give the hostname up) complete the group; every read
takes `--json`. `--env` names which control plane to talk to — a domain has
no env, only its binding does.

Why a resource, not a field: bringing a name takes an action at YOUR
registrar, then verification, then a certificate — asynchronous and
human-gated, none of which a deploy converges. It also binds to ONE env,
while an env file renders to many. And the platform may allocate a hostname
itself, which a spec field would then contradict.

If a domain command is rejected for a missing `domain:read`/`domain:write`
scope, the credential lacks it: under Reliant, grant it to your Reliant session
(Settings → Access Tokens, or `reliant auth login`); standalone, run `forge login`.
If a fresh token still lacks it, your role has no grant — an org admin can mint
a token with `domain:read,domain:write` in the web app (Settings → API tokens,
https://forge.reliantlabs.io/settings/tokens).

## `forge.OnCluster` keeps `Port.domains`

There you own the ingress, forge renders the Gateway and HTTPRoute, and
`forge.WorkloadURL` resolves the exposed port's first domain. At most 8
lowercase DNS names, no wildcards, no duplicates.

```kcl
# OnCluster only — on OnHosted this is a render error.
ports = [fw.Port {name = "http", port = 8080, expose = True, domains = ["api.hounders.club"]}]
```

## Restricting who can reach a port: `allowedSourceCIDRs`

An exposed port can declare the client addresses allowed to reach it. The check
is on the client's network address as the platform ingress sees it, and any
other address gets a 403 before the request reaches the workload. Empty or
absent means no restriction. Entries are IPv4/IPv6 CIDRs or bare addresses (a
bare address becomes a /32 or /128), at most 64, no duplicates, and only on the
`expose = True` port.

```kcl
ports = [fw.Port {
    name = "http", port = 8080, expose = True
    allowedSourceCIDRs = ["188.34.190.188/32", "65.108.48.77/32", "37.24.226.100/32", "89.246.73.244/32", "85.115.25.67/32"]
}]
```

It fails closed: only `forge.OnHosted` enforces it. `forge env render` and
`forge env deploy` refuse it on any other runtime (naming the workload, port and
runtime) rather than serve every client under a declaration that reads
"restricted". On `forge.OnCluster`, forge renders the Gateway but sets no
client-address detection, so it cannot vouch for what the gateway sees.

## Reading a domain's state

`forge env status <env>`, the post-deploy summary and `forge domain show
<hostname>` print each bound domain's state — `pending_dns` | `verifying` |
`issuing` | `live` | `failed` | `conflict` — with the DNS record still to
set and the last error:

```
custom domains (prod):
  web:
    hounders.club: pending_dns
      DNS record to set: A hounders.club → 34.63.203.181
      DNS record to set: TXT _forge-challenge.hounders.club → tok-123
    www.hounders.club: live since 2026-02-01T10:00:00Z
```

A domain that is not live is not a failed deploy: setting the record at your
registrar is your step, which is why forge prints it rather than blocking on
it. `--json` carries the same under each workload's `domains`.
