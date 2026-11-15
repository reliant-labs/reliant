---
name: targets
description: Which project files and which database `forge db seed` acts on — `-C` from any directory, seeding a throwaway postgres with a loopback `--dsn`, `--allow-remote-dsn`, and why apply fails with "seeded nothing".
---

# Which files, which database

`db/seeding` covers WHAT gets seeded. This is where it comes from and where it
goes.

## Project files come from the project root

Every `forge db` command reads `db/migrations`, `db/seeds/vocab.yaml` and
`db/seeds/custom/` from the project root: the one `-C` names, or the one the
current directory is in. A relative `--dir` is project-relative too, and a
migrations directory that does not exist is an error naming the path.

```bash
forge db migrate up -C ~/src/app --dsn "$DSN"   # from anywhere
forge db seed apply -C ~/src/app
```

## Seeding nothing is an error

`seed apply`, `seed reset` and `db reset` fail, naming the migrations directory
they read, when there was something to seed and nothing was written:

- the database has tables, but the migrations forge read define none — the
  wrong `-C` or `--dir`;
- the dev seed is scoped to tables the migrations do not define;
- the plan inserted 0 rows and every planned table is still empty.

Two outcomes are legitimately empty and succeed:
an already-seeded database (apply is idempotent: `Seeded 0 new
row(s)`), and a schema with no tables at all.

## Seeding a throwaway database

`--dsn` accepts **any loopback database**: `localhost`, `127.0.0.0/8`, `::1` or
a unix socket, on any port, with any database name. A scratch postgres needs no
special handling, which matters because port 5432 is usually taken:

```bash
DSN="postgres://postgres:postgres@localhost:55432/scratch?sslmode=disable"
forge db migrate up --dsn "$DSN"
forge db seed apply --dsn "$DSN"
```

The DSN is read the way the driver dials it: key=value DSNs, `host=` query
parameters, `PGHOST` and multi-host fallbacks all count, and every host must be
local.

A **non-loopback** `--dsn` is refused unless it is the database the env itself
declares, or you pass `--allow-remote-dsn`. The env must be dev either way, and
there is no flag for that.

Two deliberate limits:

- The relaxation applies to an explicit `--dsn` only. An exported
  `$DATABASE_URL` must still be the env's own database, because a stale export
  from another project's shell is nobody's choice.
- `forge db reset` DROPs the database, so it never takes an arbitrary loopback
  DSN. Other projects' real databases live on loopback too.
