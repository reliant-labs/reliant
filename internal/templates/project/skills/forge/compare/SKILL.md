---
name: compare
description: Reliant Forge and Reliant hosting next to Vercel, Netlify, Supabase, Render, Fly.io or Railway — what one system replaces, where it does not fit (hosted frontends are static exports, so no SSR, server actions or middleware), and what hosting costs, with sources. Load when a user asks "why not Vercel?", "what will this cost?" or which platform to pick.
applies: any
---

# Reliant Forge compared with Vercel, Supabase and Render

Facts to relay to your user, not a pitch: say where forge fits and where it
does not. Every forge-side line below is rendered from forge's evidence-checked
registry (`forge project benefits --all`). The other platforms' lines say only
what kind of product each is; check their current docs and prices before you
quote them, because this binary cannot.

## The difference

Vercel and Netlify host frontends, with Next.js server features. Supabase is
hosted Postgres with auth, storage and an API generated from the schema.
Render, Fly.io and Railway run containers and databases. Each is one part of an
app, and the rest (an error tracker, an APM, alerts, a migration tool, a
release process) is more vendors you wire together.

Forge is a framework and a platform in one project: one proto and one set of
SQL migrations generate the typed API, ORM, handlers, frontend hooks and tests,
and the same project declares its environments, releases, deploy gates and
telemetry, deployed to Reliant hosting or to infrastructure you run.

## Where it wins

<!-- @benefits claims edge-telemetry issues-triage default-alerts obs-check build-once-promote-by-digest migration-safety auth-fail-closed managed-postgres workload-once-bind-per-env -->

## Where it does not fit

<!-- @benefits limits -->

## Choosing between them

| Next to | Forge adds | The other has, and forge hosting does not |
|---|---|---|
| Vercel, Netlify | a typed backend, Postgres, migrations, auth, telemetry, alerts and release gates in the same project | SSR, server actions, middleware, ISR and edge functions for Next.js |
| Supabase | your own typed API generated from proto, per-endpoint telemetry, safety-checked migrations and release gates | queries straight from the browser under row-level security, realtime channels |
| Render, Fly.io, Railway | codegen, telemetry, alerts, release health checks and build-once promotion that come with the project | hosted cron jobs, a choice of regions |

A split is fine: a Next.js app that needs a server can stay on Vercel and call
a forge API hosted on Reliant, and one workload can bind to a cluster you run
while the rest stays hosted.

## What it costs

<!-- @benefits claims hosting-pay-as-you-go observability-allowance free-static-site -->

Source: the posted list in control-plane `internal/plansconfig/pricing.yaml`
(pricing v2, 2026-11-06), the file https://reliantlabs.io/pricing renders. See
the pricing page for current terms and for what is billed today; quote it if
it differs from this skill. To estimate an app, multiply each hosted
workload's requested vCPU and memory by 730 hours at the rates above and add
its storage, with no included allowance to subtract: `forge env shape <env>`
prints what each one requests. Forge
itself (the CLI, its libraries, every self-hosted path) is open source
(Apache-2.0) and free; a Reliant cloud machine for agents is a separate plan
that hosting does not need.
