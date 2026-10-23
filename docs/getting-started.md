# Getting started

This is the zero-to-running walkthrough for working on a forge project from
a checkout. Every command and every piece of output below was run against a
real scaffold — if something here disagrees with what your terminal says,
trust your terminal and file it.

The user guide — the same journey plus deploys, environments, observability
and troubleshooting — is at
[docs.reliantlabs.io/forge](https://docs.reliantlabs.io/forge/overview). This
page stays close to the files.

By the end you will have a service, a database-backed entity with working
CRUD, and a running stack. More importantly, you will know **which files are
yours** and which ones Forge will rewrite — the distinction the rest of your
time with Forge depends on.

## Before you start

You need Go, Docker (for the local cluster and for tests that use a real
Postgres), and the `forge` binary on your `$PATH`:

```bash
task install && forge version
```

Forge installs the proto plugins it needs on first use. If you want to check
your toolchain up front, `forge doctor` will tell you what is missing and how
to get it.

## 1. Create the project

```bash
forge project new bookmarks --mod github.com/you/bookmarks
cd bookmarks
```

You now have a project that builds and boots. There are no services yet, but
`/healthz` already serves, the command tree exists, and CI workflows,
Dockerfile, deploy environments and the local-cluster config are all in
place.

Two things are worth looking at before you write any code.

**`forge.yaml` is deliberately tiny.** Most of what other tools ask you to
configure, Forge derives from the shape of the project — the database, CI,
lint, contracts and the feature set are all inferred. You add a key only to
_override_ a default.

**Your project already knows its own conventions.** The forge binary
ships over a hundred skills covering architecture, proto, db, api, testing, deploy and
more (`forge skill list`). Reliant reads them automatically; other agents can
be told to run `forge skill search <query>`. Nothing is written into your tree
— see [Working with an agent](#7-working-with-an-agent).

## 2. Add a service

Name a service after a **domain entity** — `bookmark`, `order`, `user` — not
after the binary or the layer.

```bash
forge scaffold service bookmark
```

That writes a proto stub, the handler package, a cobra subcommand and a
test-harness entry, and appends the service's constructor to the
`cmd.Execute(...)` call in `cmd/bookmarks/main.go`:

```
✅ wired services.NewBookmarkCmd into cmd/bookmarks/main.go — `bookmarks bookmark` is live
```

`bookmarks server` serves every service on one port, so the new one is
already reachable there; the standalone `bookmarks bookmark` subcommand runs
it alone. If forge cannot find the `cmd.Execute(...)` call (you restructured
`main.go`), it leaves the file alone and prints the import and argument to
add by hand. It also binds nothing new in the deploy files: in every
environment, the binary's `server` already runs the new service.

## 3. Declare an entity

An entity is **two halves**: the wire contract in your proto, and a table in
your schema. Forge will not invent either one from the other.

You declare the message; Forge does the rest. Open
`proto/services/bookmark/v1/bookmark.proto` and add:

```proto
// forge:entity
message Bookmark {
  string url = 2;
  string title = 3;
  bool done = 4;
}
```

Note the field numbers start at 2. Forge manages `id`, `created_at` and
`updated_at` and will declare them on your message for you.

Now run bare `forge scaffold` — with no noun, it scaffolds _everything your
protos imply_:

```bash
forge scaffold
```

```
── Summary ──────────────────────────────────────────────────
  entities birthed:    1 (services.bookmark.v1.Bookmark → bookmarks)
  managed fields:      3 declared on entity messages
  quintets completed:  1
  refused:             0
```

From that one marker you got:

- **A migration** — `db/migrations/20260115093042_create_bookmarks.up.sql` (the version is a UTC timestamp, so parallel branches cannot collide), with the
  right types, `NOT NULL` defaults, and managed timestamps.
- **The CRUD quintet in your proto** — `CreateBookmark`, `GetBookmark`,
  `UpdateBookmark`, `DeleteBookmark`, `ListBookmarks`, written into the
  service you already had.
- **The entity struct and ORM** — `internal/db/bookmark_orm_gen.go`.
- **An extension point** — `internal/db/bookmark_repo_ext.go`, scaffolded
  once, for the queries generated CRUD cannot express.
- **Handler wiring** — `internal/handlers/bookmark/`, including a CRUD
  lifecycle test.

The generated migration explains its own status in a comment:

```sql
-- Born from proto message services.bookmark.v1.Bookmark (`forge scaffold entity --from-proto`).
-- This migration is YOURS from birth: forge never re-reads the proto to
-- regenerate or edit it. Evolution is a NEW migration plus a proto edit —
-- the schema truth and the wire truth evolve on independent clocks.
```

That is the central idea, stated where you will actually read it.

### Why two truths instead of one

Most codegen tools pick a single source and derive everything from it. Forge
keeps two, on purpose:

- **Proto is the wire truth.** Services, RPCs, messages.
- **SQL migrations are the schema truth.** `db/migrations/*.up.sql`.

`forge generate` applies your migrations to a **real ephemeral Postgres** and
introspects the result, so the ORM is projected from a schema that provably
exists. This is why arbitrary Postgres works — schema-qualified tables,
`JSONB`, arrays, generated columns, `CHECK` constraints — without Forge
needing to model any of it.

It also means **behavior comes from real columns**. Add a `deleted_at` column
and you get soft delete. Have `created_at` and `updated_at` and you get
managed timestamps. You never annotate for these; you write the column.

To change the schema, write a new migration. Never hand-edit the entity
struct — it is a projection, and the next generate will overwrite it.

## 4. Fill in the logic

Your handler stubs return an honest `Unimplemented` rather than a plausible
lie, and the scaffolded tests assert exactly that. This is deliberate: **the
test fails the moment you implement the handler**, forcing you to replace it
with a real assertion. There is no way to write a test row that passes no
matter what the code does.

Handlers stay thin. The shape is always the same five steps — validate,
extract auth, do the work through `s.deps`, pack the response, wrap errors:

```go
func (s *Service) GetBookmark(
	ctx context.Context,
	req *connect.Request[pb.GetBookmarkRequest],
) (*connect.Response[pb.GetBookmarkResponse], error) {
	bm, err := s.deps.Store.GetBookmark(ctx, req.Msg.GetId())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return connect.NewResponse(&pb.GetBookmarkResponse{
		Bookmark: toProto(bm),
	}), nil
}
```

`svcerr.Wrap` is not boilerplate — it is a security control. `connect.Error`'s
message is literally `err.Error()`, so any error text you did not write is
text you publish to anonymous callers. `Wrap` passes through errors you
constructed deliberately, and replaces everything else with
`internal server error` while keeping the original server-side. This exists
because the unguarded version once leaked a database DSN, password included,
to an unauthenticated caller.

If a handler grows past those five steps, move the extra logic into a
package (`forge scaffold package <name>`, which writes
`internal/<name>/contract.go`) and give the handler its interface as a
dependency. `forge lint` will tell you when a handler file grows too large.

## 5. Run it

```bash
forge env up dev
```

This is the inner loop: Postgres and the rest of the dev infrastructure in
Docker Compose, the API as a host process under air (hot reload), and the
frontends' dev servers — no cluster. It regenerates if your protos are newer
than `gen/`, applies migrations, and seeds the database so the app boots with
data in it. Every process's output also lands in `.forge/logs/dev/`, one file
per service, frontends' browser consoles included.

A cluster is involved only when something in `dev` is bound to one (an
operator, say); then the same command creates the local k3d cluster and
applies to it.

One detail that matters if you drive Forge from a script or an agent: with a
terminal attached, `env up` holds the foreground and tears the stack down on
Ctrl-C. Without one, it starts everything, prints a summary, and **returns**.
It will not hang waiting for a TTY that is not there.

Host readiness includes compilation: Forge waits up to two minutes for the
declared ports, prints progress, and reports a runner's exit immediately.
On a slower machine, use `forge env up dev --host-ready-timeout 5m`. A timeout reports the pending ports and log paths;
it does not mean the process crashed, and started processes remain tracked
for `forge env status` and `forge env down`.

To see what is actually running — ports, log files, whether the binary is
stale relative to your last commit:

```bash
forge env status dev
```

## 6. The gate

Before you call a change done:

```bash
forge generate && forge lint && go build ./... && task test
```

Run it at every step, not at the end. `forge lint` catches contract gaps,
handlers doing business logic, concrete types where interfaces belong, and
migration hazards. Catching one of those at introduction is cheap; finding
six of them stacked up is not.

Three advisory rules are worth running deliberately on code you or an agent
have been iterating in. They are warnings only, so the gate above will not
surface them:

```bash
forge lint --config-deps          # scalars in Deps that should be typed config
forge lint --optional-deps-guard  # unguarded use of an optional dependency
forge lint --frontend-stores      # server data living in a Zustand store
```

`--config-deps` in particular writes the fix out for you — the proto message,
the `AppConfig` line, and the Go replacement, per finding.

`task test` is the suite CI runs — Go with the race detector against a real
Postgres, plus every frontend's tests — with the database and build tags set
up for you. While iterating, `task test:short -- ./internal/handlers/bookmark/...`
runs one package's fast tests, cached. `task ci` runs the whole pull-request
gate locally.

## 7. Working with an agent

If an LLM is writing most of this code, the following are the parts that keep
it honest over months rather than minutes.

**Point it at the skills.** `forge skill list` names them; `forge skill load
<name>` prints one. They are embedded in the binary, so they always match the
version you are running. A copy on disk can be older — when they disagree,
`forge skill load` wins.

**Let it ask the binary instead of guessing.** Four commands enumerate
Forge's whole surface from its own command tree and descriptors, so they
cannot drift the way prose can:

```bash
forge project capabilities      # every verb, every lint analyzer, every marker
forge project annotations       # how proto fields become columns and validators
forge project annotations --kind go   # the // forge:* markers in Go source
forge project libraries         # every forge/pkg library, and where it lives
forge project audit --json      # what this project currently is
```

When you hit a `// forge:optional-dep` or `// forge:constructor` in
scaffolded code and want to know what it does, `--kind go` is the answer.

`forge project libraries` is the highest-value one. The single most
re-implemented thing in Forge's history is a utility that already existed in
`forge/pkg`. Check before porting, and read a package's API with
`forge project libraries <pkg>` — every exported signature, methods included —
rather than searching the filesystem.

**Trust the errors.** Forge's failures are written as runbooks — what was
expected, what was found, and the literal command that fixes it:

```
Error: forge scaffold rpc nosuchsvc DoThing: service "nosuchsvc" has no
handler directory at internal/handlers/nosuchsvc.
Fix: run `forge scaffold service nosuchsvc` first to scaffold the service.
```

An agent can act on that without a round trip to you.

## 8. What is yours and what is Forge's

This is the thing to internalize.

There are really three categories, not two.

| Category                     | Examples                                                                                                                                                     | Behavior                                  |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------- |
| **Regenerated every run**    | `gen/**`, `*_gen.go` (including `internal/db/<entity>_orm_gen.go`), `deploy/kcl/config_gen.k`                                                                | Overwritten. Never edit.                  |
| **Written once, then yours** | `internal/app/compose.go`, `internal/app/auth.go`, `db/migrations/*.sql`, `internal/db/<entity>_repo_ext.go`, `deploy/kcl/<env>/config.k`                    | Forge writes if absent, then leaves alone |
| **Purely yours**             | handler bodies (`internal/handlers/<svc>/rpc_<name>.go`), a domain package's `internal/<name>/contract.go`, `internal/app/providers.go`, `cmd/<bin>/main.go` | Forge never writes these                  |

You do not have to memorize it. Files in the first category say so in their
own header:

```go
// Code generated by forge. DO NOT EDIT.
// forge:hash=528a683f9b60023d43a386beaeb374971eca4850867a3188d66f2af4859b3d89
// forge-owned: regenerated every run — do not edit
```

And `forge project map --json` will tell you the ownership of any file in the
tree.

Two consequences worth knowing now:

**Hand-editing a generated file stops the next generate.** Forge notices the
hash no longer matches and refuses rather than silently reverting your work.
Move the change to an extension point, or take ownership of the file
deliberately:

```bash
forge project disown internal/db/bookmark_orm_gen.go --reason "why the generated form doesn't fit"
```

The `--reason` is required, and it is not bureaucracy: a disown means the
generated code could not express something you needed, and that is the most
useful feedback Forge can get. Reach for it rarely — a disowned file stops
receiving improvements, so you inherit its maintenance. (You can hand a file
back by deleting it and regenerating, which discards your version.)

**On a real application the split is lopsided in your favour.** A
14-service production app built with Forge measures 1081 files owned by the
team against 139 regenerated. Forge holds the skeleton; you write the
program.

## Where to go next

```bash
forge skill load architecture   # the deep version of the layer map
forge skill load db             # migrations, projections, CRUD overrides
forge skill load api            # handler patterns, error handling, testing
forge skill load auth           # the auth seam and the dev-loop IdP
forge skill load deploy         # environments, releases, plans, promotion
forge skill load testing        # the test pyramid and the harness patterns
```

If you are migrating an existing codebase rather than starting fresh, start
with `forge skill load migration`.
