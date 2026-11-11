---
name: observability/uptime
description: Uptime checks — outside-in HTTP probes of a hosted env's public frontends and routes, run by the platform on a schedule. `forge.UptimeCheck` / `forge.Uptime` in KCL (a url, a route, or a hosted target; method, interval, timeout, expected status and body), the checks forge derives with no declaration and how to opt out, the `uptime.check.up` / `uptime.check.duration` / `tls.cert.expiry_seconds` metrics, and the UptimeCheckFailing / CertificateExpiring alerts.
---

# Uptime checks

`ServiceSilent` notices that traffic stopped. It says nothing when nobody is
visiting. An uptime check asks the question from outside: *does this address
answer, right now, the way it should?*

The platform's prober requests each check on a schedule and writes the result
into the env's telemetry, so a check is queryable, alertable and visible to the
agent that triages the alert.

## What you get with no declaration

A **hosted** env (one with a `control_plane`, and not `lifecycle = "local"` or
`"ephemeral"`) is probed with nothing declared. Forge derives one check per:

| thing | check | request |
|---|---|---|
| `forge.OnHosted` frontend | `<frontend>` | `GET /` at its platform hostname |
| `forge.OnHosted` workload with an `expose = True` port | `<workload>` | `GET /` at its platform hostname |
| HTTPRoute whose route, listener or gateway names a host | `route-<route>` | `GET` its public URL; any answer below 500 is up (a route's own path may 404 or ask for a login) |

Every default is 60s interval, 10s timeout, and "up" is a 2xx or 3xx. They are
recorded in the release shape (`shape.uptime_checks`, `source: default`); the
control plane reads them from there.

A route no hostname names (only `localhost`) gets no check: `localhost` is not
a public address. A local env, and an env with no control plane, derives nothing.

## Declaring a check

```python
uptime = forge.Uptime {
    checks = [
        forge.UptimeCheck {
            name = "checkout"
            url = "https://shop.example.com/healthz"   # a public address
            expect_body = "ok"                          # the body must contain it (GET only)
        }
        forge.UptimeCheck {name = "billing", route = "billing"}                  # an HTTPRoute of this Bundle
        forge.UptimeCheck {name = "api-ready", target = "api", path = "/readyz"}  # a hosted workload or frontend
    ]
}
```

Exactly one target:

- `url` — an absolute http(s) address on the public internet, no credential.
- `route` — the name of an HTTPRoute in this Bundle; its public URL is the target.
- `target` — a hosted frontend or workload. The platform allocates its
  hostname, so the control plane resolves it; `path` is requested on it.

| field | default | |
|---|---|---|
| `method` | `GET` | `GET` or `HEAD` |
| `interval` | `60s` | `30s` to `10m` |
| `timeout` | `10s` | `1s` to `1m`, and shorter than the interval |
| `expect_status` | any 2xx or 3xx | statuses (`"200"`) or classes (`"2xx"`) |
| `expect_body` | none | substring, up to 256 bytes; needs `GET` |

A check you declare with a derived check's name replaces it. Regions are not
declared: v1 probes from the platform's own cluster.

## Opting out

```python
uptime = forge.Uptime {defaults = False}            # no derived checks (yours stay)
uptime = forge.Uptime {skip = ["route-admin"]}      # all but one
```

`skip` names derived checks; a name that matches none is refused at render.
An env with no checks has no uptime alerts.

## Only public addresses

The prober dials public endpoints only. A `url` naming a loopback, private,
link-local, cloud-metadata or cluster-internal host (including a name that
resolves to one) is refused when you declare it (`forge env render`), and the
prober's dialer refuses it again at connect time. A check is not a way to reach
inside the platform.

## What a check writes

Three gauges per check, one sample per probe, on the env's own telemetry
(`deployment.environment.name`), attribute `check`:

| metric | meaning |
|---|---|
| `uptime.check.up` | 1 passed, 0 failed. Carries `http.response.status_code`, and `error.type` when it failed: `timeout`, `dns`, `connect`, `tls`, `refused`, `status`, `body` |
| `uptime.check.duration` | seconds, request to end of response (or to the failure) |
| `tls.cert.expiry_seconds` | seconds until the served certificate expires; negative once it has. https checks only |

```
forge obs metrics query prod uptime.check.up --group-by check --since 3h
```

## Alerts

Two defaults, each with a warning and a critical level, only for an env that
has checks (see `observability/alerts` for tuning):

- `UptimeCheckFailing` (warning) after 3 failed probes in a row,
  `UptimeCheckDown` (critical) after 10.
- `CertificateExpiring` (warning) within 14 days, `CertificateExpiringSoon`
  (critical) within 3 days, an expired certificate included.

Both carry the why-fired payload: the last status (or how the probe failed),
the latency, the certificate's expiry and when the failure began.

A prober that stops writing is not every check failing: `UptimeCheckFailing`
ignores a check whose newest probe is more than 15 minutes old. The platform
watches the prober itself with an external heartbeat.
