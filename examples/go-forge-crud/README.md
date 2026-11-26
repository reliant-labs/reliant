# go-forge-crud

[![Deploy with Reliant Forge](https://raw.githubusercontent.com/reliant-labs/forge/main/docs/assets/deploy-with-reliant-forge.svg)](https://docs.reliantlabs.io/forge/deploy-without-an-account)

A Go API on forge's own stack: one entity, `Task`, with a Postgres table and a
Connect RPC CRUD API (`CreateTask`, `GetTask`, `UpdateTask`, `DeleteTask`,
`ListTasks`), all generated from a five-line proto message.

**This directory holds a script, not the app.** The generated project is about
170 files, nearly all generated, so `scaffold.sh` makes it instead: it runs
`forge project new --service tasks --claimable`, appends the one hand-written
input below to the service's proto, and runs `forge scaffold`.

```proto
// forge:entity
message Task {
  string title = 2;
  bool done = 3;
}
```

From that, `forge scaffold` writes the create-table migration, the CRUD rpcs
and their messages, the ORM, the handlers and a lifecycle test.

## Prerequisites

Go, Node (for `npx`), and `reliant forge` or `FORGE="npx -y @reliantlabs/forge@latest"`.

## Make it, and run it locally

```bash
./scaffold.sh tasks-api          # or: FORGE="npx -y @reliantlabs/forge@latest" ./scaffold.sh tasks-api
cd tasks-api
reliant forge env up dev         # Postgres, migrations, the API; prints the URL
```

## Deploy it

```bash
npx -y @reliantlabs/forge@latest env deploy prod --claimable --plan-only
npx -y @reliantlabs/forge@latest env deploy prod --claimable
```

`--claimable` on `project new` sized prod for the anonymous plan (one service,
its migration job and one database; 125m / 128 MiB; no outbound internet).
Without it, prod asks for 250m / 1 GiB and a 10 GiB database, and
`--claimable` refuses it. Claimed, or deployed signed in, it needs billing.

## Verify it

The CRUD rpcs keep forge's default `auth_required: true`, and this project has
no sign-in, so an anonymous call answers 401, which shows the API is up and
fails closed:

```bash
curl -s -X POST https://<your-host>/services.tasks.v1.TasksService/ListTasks \
  -H 'content-type: application/json' -d '{}'
```

To call it without a token, set `auth_required: false` on the rpcs you want
public and run `reliant forge generate`, as
[`nextjs-static-go-api`](../nextjs-static-go-api/) does.

Verified on 2026-11-25: `scaffold.sh` ran end to end (`go build ./...` passes)
and `--claimable --plan-only` fits the plan. Not deployed for real, and
`env up dev` was not run.
