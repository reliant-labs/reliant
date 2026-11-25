# nextjs-static-go-api

[![Deploy with Reliant Forge](https://raw.githubusercontent.com/reliant-labs/forge/main/docs/assets/deploy-with-reliant-forge.svg)](https://docs.reliantlabs.io/forge/deploy-without-an-account)

A full-stack forge app: a Go Connect RPC API with one entity (`Note`) in
Postgres, and a Next.js static export that lists, creates, views and edits
notes through generated TypeScript hooks. Hosting serves the frontend and
routes `/_api` to the API on the same origin.

**This directory holds a script, not the app.** The generated project is about
440 files, so `scaffold.sh` makes it: `forge project new --service notes
--frontend web --auth-mode none --claimable`, then the one entity, then
`forge scaffold`, then a build of the API and the frontend.

`--auth-mode none` gives an app nobody signs in to. `forge scaffold` still
writes the CRUD rpcs with `auth_required: true` (so each would answer 401), so
the script switches them to `false` and regenerates: the result is a public
notes board. Anyone with the URL can write to it.

## Prerequisites

Go, Node 20 or newer, and `reliant forge` or `FORGE="npx -y @reliantlabs/forge@latest"`.

## Make it, and run it locally

```bash
./scaffold.sh notes
cd notes
reliant forge env up dev         # Postgres, the API, the Next dev server; prints the URLs
```

## Deploy it

```bash
npx -y @reliantlabs/forge@latest env deploy prod --claimable --plan-only
npx -y @reliantlabs/forge@latest env deploy prod --claimable
```

Unclaimed: one service with its migration job, one database and one static
site, 125m / 128 MiB, 72 hours, no outbound internet. Claimed, or deployed
signed in, the static site is free in your free hosting organization (one per
person) and the API and database need hosting billing.

## Verify it

Open `https://<your-host>/notes`, create a note, reload. Or call the API:

```bash
curl -s -X POST https://<your-host>/_api/services.notes.v1.NotesService/ListNotes \
  -H 'content-type: application/json' -d '{}'
```

Verified on 2026-11-25: `scaffold.sh` ran end to end (`go build ./...` and
`npm run build` pass) and `--claimable --plan-only` fits the plan. Not deployed
for real from this script, and `env up dev` was not run.
