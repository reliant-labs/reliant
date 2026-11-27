# llm-app

[![Deploy with Reliant Forge](https://raw.githubusercontent.com/reliant-labs/forge/main/docs/assets/deploy-with-reliant-forge.svg)](https://docs.reliantlabs.io/forge/deploy-without-an-account)

A small Node service that asks an LLM for a haiku (`GET /?topic=...`). It holds
no provider key and no gateway URL:

- each env declares `llm = forge.LLM {...}` in `deploy/kcl/<env>/main.k`:
  Reliant's gateway (`forge.OnHosted`) on staging and prod, the same gateway
  with your own token (`forge.LLMEndpoint`) in dev;
- the workload takes `forge.llm_env()` (`deploy/kcl/workloads.k`), which sets
  `OPENAI_BASE_URL`, `OPENAI_API_KEY` and their Anthropic twins;
- `server.js` uses the stock `openai` SDK, which reads those variables itself.
  The model is `LLM_MODEL` (default `claude-haiku-4-5`).

`reliant forge project new --in-place --kind app` wrote the rest; the `llm`
lines, `forge.llm_env()` and the `/healthz` probe were added by hand (it does
not detect an LLM SDK).

## Caller authorization

The haiku endpoint spends the organization's budget. It requires
`Authorization: Bearer <APP_API_TOKEN>`: a separate, high-entropy application
secret shared only with callers you authorize to spend that budget. Never put it
in a browser bundle or reuse the gateway key. Requests with missing/wrong tokens
return 401 without calling the gateway; an unconfigured token returns 503.
`/healthz` remains public and never calls the gateway.

Generate a random token (for example `openssl rand -hex 32`), then save it via
`reliant forge secret set --env dev APP_API_TOKEN` (and likewise for staging/prod).
Keep the same value in your calling shell as `APP_API_TOKEN`. Rotate it if exposed.
This small example has one authorized caller class, not per-user accounts.

## It needs a claimed deploy (or an account) to answer

An **unclaimed** `--claimable` deploy has **no outbound internet**, and the LLM
variables of an unclaimed pod stay blank. So deployed anonymously, the service
runs and `/` answers **503 "LLM unavailable"** (by design: it degrades rather
than crash). Once the deploy is claimed, or deployed signed in, the platform
fills in a per-environment key and `/` returns a haiku to authorized callers.
The responses below assume a configured, valid application token; without one,
no gateway call is possible.

|        | Unclaimed           | Claimed / signed in                                     |
| ------ | ------------------- | ------------------------------------------------------- |
| `/`    | 503 LLM unavailable | a haiku                                                 |
| models | none                | the curated catalog; `claude-haiku-4-5` by default here |
| spend  | none                | $50 per env per 30 days by default, billed to the org   |

## Prerequisites

Node 22, Docker. For dev, a Reliant access token with the `llm:invoke` scope
(from the Reliant console's access-token page), stored with
`reliant forge secret set --env dev RELIANT_LLM_API_KEY`. It is billed to you.

## Run it locally

```bash
npm install
reliant forge env up dev
curl -H "Authorization: Bearer $APP_API_TOKEN" 'localhost:<port>/?topic=postgres'
```

## Deploy it

```bash
# signed in: the LLM works
reliant forge env deploy prod --yes
# no account: it deploys, and / answers 503 until claimed
npx -y @reliantlabs/forge@latest env deploy prod --claimable
```

## Verify it

```bash
curl https://<your-host>/healthz             # {"ok":true}
curl -H "Authorization: Bearer $APP_API_TOKEN" 'https://<your-host>/?topic=deploys'
# a haiku with gateway access; otherwise 503. Without caller credentials: 401/503.
node --test server.test.js                 # authorization / no-spend regression tests
node --test deployment.test.js             # prod/staging managed caller-secret wiring (requires Reliant CLI)
```

A healthy rollout does not prove model access; only a haiku does.

Verified on 2026-11-25: Docker build and run with no key (`/` answers 503),
`reliant forge env render dev` and `prod`, and `--claimable --plan-only`.
**Not verified:** a real model response. That needs a claimed deploy or an
`llm:invoke` token, and neither was used.
