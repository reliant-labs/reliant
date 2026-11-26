# fastapi-postgres

[![Deploy with Reliant Forge](https://raw.githubusercontent.com/reliant-labs/forge/main/docs/assets/deploy-with-reliant-forge.svg)](https://docs.reliantlabs.io/forge/deploy-without-an-account)

A FastAPI notes API on Postgres, deployed from its own Dockerfile. It shows how
forge adopts an existing app in another language without changing it:
`reliant forge project new --in-place --kind app` read `Dockerfile` and
`docker-compose.yml` and wrote `forge.yaml` and `deploy/kcl/`:

- the `api` workload, built from the Dockerfile, port 8000, ready when
  `/healthz` answers (that probe was edited in by hand; the command wrote a TCP probe);
- a managed Postgres on staging and prod, handed to the app as `DATABASE_URL`;
- a `migrate` job (`python -m app.migrate`, from the compose file's `migrate`
  service) that runs from the same image before every rollout;
- prod sized for an anonymous deploy (`--claimable`).

## Prerequisites

Docker, and Node (for `npx`). Python is only needed to run it outside Docker.

## Run it locally

```bash
docker compose run --rm --build migrate && docker compose up --build api
curl -X POST localhost:8000/notes -H 'content-type: application/json' -d '{"body":"hello"}'
curl localhost:8000/notes
```

`reliant forge env up dev` runs the same compose file.

## Deploy it

```bash
npx -y @reliantlabs/forge@latest env deploy prod --claimable --plan-only   # check; creates nothing
npx -y @reliantlabs/forge@latest env deploy prod --claimable
```

Unclaimed, it lives 72 hours, gets 125m CPU / 128 MiB and 256 MiB of Postgres
data, and has no outbound internet (this app needs none). Claim it, or deploy
signed in (`reliant forge env deploy prod --yes`), to keep it; a backend and a
database need billing on your organization. See [the examples
README](../README.md#deploy-one-with-no-account) for the full comparison.

## Verify it

```bash
curl https://<your-host>/healthz                     # {"ok":true}: the app is up and reached its database
curl -X POST https://<your-host>/notes -H 'content-type: application/json' -d '{"body":"hello"}'
curl https://<your-host>/notes                       # the note you wrote
```

Verified on 2026-11-25: built and run in Docker against `postgres:16`, then
deployed with `--claimable` (rollout succeeded; `/healthz`, `POST /notes` and
`GET /notes` answered on the live URL).
