# express-postgres

[![Deploy with Reliant Forge](https://raw.githubusercontent.com/reliant-labs/forge/main/docs/assets/deploy-with-reliant-forge.svg)](https://docs.reliantlabs.io/forge/deploy-without-an-account)

An Express app that counts visits in Postgres, deployed from its own
Dockerfile. Unlike [`fastapi-postgres`](../fastapi-postgres/) it has no compose
file, so `reliant forge project new --in-place --kind app` worked from the app
alone:

- the `api` workload from the Dockerfile's `EXPOSE 3000`;
- a managed Postgres on staging and prod, because `package.json` depends on
  `pg`, handed to the app as `DATABASE_URL`;
- a `migrate` job (`npm run migrate`, from the `migrate` script) before every
  rollout;
- dev runs `npm start` on your machine beside a forge-run Postgres.

The `/healthz` readiness probe was edited in by hand (the command wrote a TCP probe).

## Prerequisites

Node 22 and Docker. `reliant forge` (or `npx -y @reliantlabs/forge@latest`) for dev.

## Run it locally

```bash
npm install
reliant forge env up dev        # Postgres, the migrate job, then `npm start`; prints the URL
```

## Deploy it

```bash
npx -y @reliantlabs/forge@latest env deploy prod --claimable --plan-only   # check; creates nothing
npx -y @reliantlabs/forge@latest env deploy prod --claimable
```

Unclaimed, it lives 72 hours, gets 125m CPU / 128 MiB and 256 MiB of Postgres
data, and has no outbound internet (this app needs none). Claim it, or deploy
signed in, to keep it; a backend and a database need billing. See [the
examples README](../README.md#deploy-one-with-no-account).

## Verify it

```bash
curl https://<your-host>/healthz   # {"ok":true}
curl https://<your-host>/          # {"message":"hello from express","visit":1}, counting up
```

Verified on 2026-11-25: built and run in Docker against `postgres:16` (migrate,
`/healthz`, `/`), `reliant forge env render prod`, and `--claimable
--plan-only` (fits the plan). Not deployed for real, and `forge env up dev`
was not run.
