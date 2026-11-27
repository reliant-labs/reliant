---
name: observability/status-page
description: A status page for an app's own users, built on its uptime checks. `forge.StatusPage` in the env's KCL (title, logo, links, accent colour, and components that each name uptime checks), what render refuses, the `status_page` release-shape field, the forge.status/v1 document and the `forge/pkg/statuspage` renderer (index.html, feed.atom, status.json), and what a public page never shows.
---

# Status page

A status page tells an app's own users whether the app works: one row per
component, its current state, 90 days of daily availability bars, and the
incidents and maintenance the team published. Its numbers come from the env's
uptime checks (`observability/uptime`), so it needs no second monitor.

It is **opt-in only**. An env that does not declare `forge.StatusPage` gets no
page: nothing is rendered, recorded or published, and no scaffold adds one.
Declaring a page does not publish it either (see below).

**What exists (v1.12.0 and later):** the declaration and its render checks, the
`status_page` field of the release shape, and the `forge/pkg/statuspage`
document and renderer. **Not built:** hosting a public page for a customer's own
app, publishing incidents and maintenance, and serving it from a hostname. The
"going public" and "incidents" paragraphs below describe the design; do not
promise them to a user. Reliant's own page (status.reliantlabs.io) uses the same
document format from its own control-plane endpoint.

## Declaring the components

```python
uptime = forge.Uptime {
    checks = [forge.UptimeCheck {name = "api-ready", target = "api", path = "/readyz"}]
}
status_page = forge.StatusPage {
    title = "Acme status"
    logo_url = "https://acme.com/logo.svg"         # https only
    accent_color = "#5b21b6"                       # decoration only, never a text colour
    links = [forge.StatusLink {label = "Support", url = "https://acme.com/help"}]
    components = [
        forge.StatusComponent {name = "Website", checks = ["web"]}
        forge.StatusComponent {name = "API", checks = ["api-ready", "route-billing"], description = "REST and webhooks"}
    ]
}
```

A component is a public name for one or more checks of this env, and its state
is the worst of theirs. The page is opt-in per component: a check no component
names never appears on it, so a derived `route-admin` check cannot leak.

`checks` must name checks in the env's final list: the derived ones
(`<frontend>`, `<workload>`, `route-<route>`), minus `Uptime.skip`, plus
`Uptime.checks`. Render refuses any other name and lists the checks the env
has. Other rules: 1 to 50 components with unique names (up to 64 characters),
a description up to 200 characters, a title up to 100, at most 10 links, and
http(s) link URLs with no credential.

The resolved page is recorded in the release shape as `status_page` (checks
sorted, components in your order). `forge env render <env>` shows it.

## What is not declared

Whether the page is public. Going public is a separate, audited act by a
person in the control plane, like binding a domain, so rolling back a release
can neither publish nor unpublish a page. Incidents, maintenance windows and their updates are live
control-plane state too, written by people (agents may draft).

## States

| state | when |
|---|---|
| operational | every check's newest probe passed, and no check has an open run of failures |
| degraded | a recent probe failed, but no run reached 3 failures |
| outage | an uptime incident is open on a check (3 or more failed probes in a row) |
| maintenance | a published maintenance window covering the component is active |
| no data | the newest probe is older than 15 minutes, or there are none; never shown as operational |

A person may override a component's state (a single-region false positive);
the page labels it "set manually". The page shows each state as an icon and a
word, never colour alone.

## The document and the renderer

The page is one JSON document, `forge.status/v1`, and one renderer, both in
`forge/pkg/statuspage` (`forge project libraries statuspage` prints the API).
`statuspage.Render(doc)` validates the document and returns `index.html`
(no JavaScript, no third-party script, a strict CSP), `feed.atom` (one entry
per incident and maintenance window) and the canonical `status.json`. The same
document gives the same bytes. `statuspage.Decode` reads a `status.json` back
and refuses an unknown schema or field. With no Reliant account, fill a
document from your own monitor and render the same page.

The document is an allowlist. It carries the page's title, logo, links and
accent colour, component names, descriptions and states, daily availability,
and published incident and maintenance text with times. It never carries check
names, URLs or hostnames, HTTP statuses, error types, latency, certificate
expiry, env, org or project ids, alert names, issue titles, who published an
update, whether an agent drafted it, or any draft. Update bodies are plain
text: blank lines make paragraphs, and every character is escaped.

Probes run from one region (the platform's cluster), and the page is served by
the same platform as a hosted app, so an outage of the platform's static
hosting takes the page down with the site.
