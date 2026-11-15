---
name: migration-v1.0.0-image-registries
description: Move a v0.1.46 docker-compose.yml off Docker Hub — postgres from public.ecr.aws/docker/library, the OpenTelemetry collector from ghcr, ClickStack through mirror.gcr.io, Zitadel pinned by digest — so a shared IP's anonymous pull limit cannot stop forge env up.
relevance: migration
---

# v1.0.0 — dev images no longer pull from Docker Hub

Use when an `image:` in `docker-compose.yml` names no registry host, such as
`postgres:17-alpine`, `clickhouse/clickstack-local:…` or `otel/…`.

## What changed

Docker Hub limits anonymous pulls per IP address. An office, a CI runner or a
cloud sandbox shares one address across many users, so a first
`forge env up` can fail partway through with `toomanyrequests`.

A scaffolded `docker-compose.yml` now pulls nothing from Docker Hub, and every
image is pinned by digest. With a digest pin, the registry only transports the
image: what runs is what the digest names, whichever host serves it.

| Service | Before | Now |
|---|---|---|
| `postgres`, `idp-db` | `postgres:17-alpine` | `public.ecr.aws/docker/library/postgres:17-alpine@sha256:1bcc18d8…` (the Docker Official Images mirror) |
| `clickstack`, `profiles-schema` | `clickhouse/clickstack-local:2.40.0@sha256:4690fbfc…` | `mirror.gcr.io/clickhouse/clickstack-local:2.40.0@sha256:4690fbfc…` (Google's Docker Hub cache; ClickStack is published only to Docker Hub) |
| `otel-forwarder`, `otel-profiles` | `otel/opentelemetry-collector-contrib:0.162.0@sha256:39923a8e…` | `ghcr.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0.162.0@sha256:39923a8e…` |
| `idp` | `ghcr.io/zitadel/zitadel:v4.19.5` | the same, plus `@sha256:a586456a…` |

The digests are unchanged: each registry serves the same bytes Docker Hub did.
The node agent's collector, `OTEL_COLLECTOR_IMAGE` in forge's KCL, moved to the
same ghcr reference. `e2e/<svc>/docker-compose.e2e.yml` uses the same postgres
reference.

## Steps

1. `forge project upgrade` rewrites `docker-compose.yml` for you when you have
   not edited it.
2. If you have edited it, copy each `image:` line from a fresh
   `forge project new` (the full digests are there), and do the same in each
   `e2e/<svc>/docker-compose.e2e.yml`.
3. To pull through your own mirror instead, change only the host and keep the
   digest.
4. Run `forge env up dev`. The first run pulls each image once from its new
   host.
