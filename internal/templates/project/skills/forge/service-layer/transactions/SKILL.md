---
name: transactions
description: Transactions in a forge service — s.deps.DB.RunTx(ctx, orm.Serializable|orm.ReadCommitted, fn) carries the transaction in ctx so stores and generated delegates join it; there is no default isolation, and choosing the level is choosing a correctness argument (no locks + retry, or a lock + one attempt); orm.AfterCommit for side effects; the lock-free state-transition recipe; BeginDetached; how RunTransaction/WithTx relate.
---

# Transactions: `RunTx` carries the transaction in `ctx`

**The transaction lives on `DB orm.Context`, not on the store.** The store is
the data surface; `orm.Context` owns the transaction. Declare both when a use
case writes atomically — `DB orm.Context` is the canonical field forge already
wires, so it costs nothing:

```go
type Deps struct {
    DB        orm.Context      // the transaction boundary: DB.RunTx(ctx, orm.Serializable, …)
    Estimates db.EstimateStore // the data surface
    Jobs      db.JobStore
}

err := s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
    est, err := s.deps.Estimates.GetEstimateByID(ctx, id) // joins: ctx carries the tx
    if err != nil {
        return err
    }
    if err := s.deps.Estimates.UpdateEstimate(ctx, est); err != nil {
        return err
    }
    return s.deps.Jobs.CreateJob(ctx, job)
})
```

Every query made with the `ctx` fn receives — store methods, generated
delegates (`db.GetJobByID(ctx, s.deps.DB, id)`), `pkg/crud`, `db.Bun()`
builders and raw SQL through `s.deps.DB` — runs inside the transaction. There
is no handle to thread and nothing to rebind. A `RunTx` whose `ctx` already
carries a transaction on the same database **joins** it (its options are
checked, then ignored), so a service method that wraps its own work in `RunTx`
composes into a caller's larger one unchanged. A transaction is only ever used
for its own database: a `ctx` carrying one for database A never runs a
statement sent to B.

## Choose the level: there is no default

Every writable transaction names its isolation, and with it its attempt
budget — `RunTx(ctx, orm.Serializable, fn)`, `RunTransaction(ctx, orm.ReadCommitted, fn)`,
`BeginDetached(ctx, orm.ReadCommitted)`. An unset level is rejected with
`orm.ErrInvalidTxOptions`, naming the call site. The two levels make
**opposite** correctness arguments, so neither is safe to inherit:

| Spelling | Level, attempts | Correct because | Use for |
|---|---|---|---|
| `orm.Serializable` | SERIALIZABLE, retried on `40001`/`40P01` up to `orm.DefaultTxMaxAttempts` (7) | the database aborts the loser of any race and the retry re-reads committed state | read-check-write with **no locks** — the default choice for a state transition |
| `orm.ReadCommitted` | READ COMMITTED, 1 attempt | a **lock** (`SELECT … FOR UPDATE`, `pg_advisory_xact_lock`) makes writers take turns, or the work is one statement | lock-based code, single-statement writes, counters allocated in one `UPDATE … RETURNING` |
| `orm.RepeatableRead` | REPEATABLE READ, 7 attempts | one snapshot for the whole transaction | multi-statement reads/reports that write; it does not prevent write skew |

Refine one with `.WithMaxAttempts(n)` (`orm.ReadCommitted.WithMaxAttempts(3)`
to survive deadlocks) and `.WithReadOnly()`, which return an `orm.TxOptions`
(also accepted, written out, as `orm.TxOptions{Isolation: orm.ReadCommitted, MaxAttempts: 3}`). A transaction that only reads takes
no level: `s.deps.DB.RunTxReadOnly(ctx, fn)` is SERIALIZABLE READ ONLY
DEFERRABLE — one consistent snapshot, no predicate locks, never aborted —
because postgres rejects every write in it, so there is no lost update to
choose a defence against.

**Never mix the two arguments.** A lock inside `orm.Serializable` is the trap:
the snapshot is taken before the lock wait, so the writer that waited wakes
into stale data and fails `40001` — it survives only by retrying, and with one
attempt it simply fails. `forge lint --conventions` reports it as
`forgeconv-tx-lock-under-serializable` (an error under `--strict`). Pick one:
keep the lock and run at `orm.ReadCommitted`, or keep `orm.Serializable` and
delete the lock. And the reverse: read-check-write at `orm.ReadCommitted`
**without** a lock double-applies under concurrency — pkg/crud's
`TestRunTransaction_Premise_ReadCommittedReadCheckWriteDoublePays` shows two
payments landing on one balance.

**Your own opener is judged too, once you declare it.** A wrapper that takes
an `orm.TxPolicy` and runs the transaction itself — a repository `RunTx` with
a retry ladder of its own — resolves the policy with `orm.ResolveTxPolicy`
(`orm.ResolveDetachedTxPolicy` when it hands the transaction to its caller),
which refuses a blank level exactly as `RunTx` does, and says what it is on
its declaration:

```go
//forge:tx-opener level-arg=1
func (r *Repo) RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error {
	opts, err := orm.ResolveTxPolicy("Repo.RunTx", policy)
	…
}
```

`level-arg` is the 0-based index of the argument naming the policy (`ctx` is
0; a receiver is not counted). The body is the last func-typed parameter
(`body-arg=<n>` to name another); an opener with none is detached, and the
rest of the function that opened it is its body. The lint then matches calls
to it by name and arity, project-wide, as it matches pkg/orm's. A marker it
cannot read — or one not on a func declaration — is an error, not a no-op.

## What `RunTx` guarantees, and what it asks of you

- **It runs `fn` up to `opts.MaxAttempts` times**, with jittered backoff, while
  it fails with serialization failure or deadlock (SQLSTATE `40001` /
  `40P01`). Still conflicting after the last attempt → a `svcerr.Aborted`
  error (the driver error kept as its cause), which `svcerr.Wrap` maps to
  `CodeAborted`.
- **With `orm.Serializable`, `fn` may run more than once.** A retry rolls back
  the failed attempt's database work and calls `fn` again from the top, so
  `fn` must have no effect outside the database. Send the email, publish the
  event, enqueue the job with `orm.AfterCommit(ctx, func(ctx context.Context) { … })`
  — it runs once, after the outermost commit; a rolled-back or retried attempt
  discards it. Outside a transaction `AfterCommit` returns
  `orm.ErrNoTransaction`.
- `fn`'s own error comes back verbatim, so your sentinels survive.
- The transaction is one connection: don't fan `ctx` out to goroutines inside
  `fn`.

## Recipe: a state-transition RPC

Approve an estimate, pay an invoice, move a job to SCHEDULED: read the row,
check the transition is legal, write the change. That is the whole method —
**no `SELECT … FOR UPDATE`, no per-entity `Lock<Entity>` helper:**

```go
func (s *svc) RecordPayment(ctx context.Context, in RecordPaymentInput) (Invoice, error) {
    var out *db.Invoice
    err := s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
        inv, err := s.deps.Invoices.GetInvoiceByID(ctx, in.InvoiceID) // get
        if err != nil {
            return err
        }
        if inv.Status != "SENT" {                                        // check
            return svcerr.FailedPrecondition("only a sent invoice can take a payment")
        }
        if in.AmountCents > inv.BalanceCents {
            return svcerr.FailedPrecondition("payment exceeds the balance")
        }
        inv.PaidCents += in.AmountCents                                  // masked update
        if err := s.deps.Invoices.UpdateInvoiceMasked(ctx, inv, []string{"paid_cents"}); err != nil {
            return err
        }
        out = inv // generated balance_cents was scanned back by the write — no re-read
        return orm.AfterCommit(ctx, func(ctx context.Context) {
            s.deps.Events.PaymentRecorded(ctx, inv.ID) // side effect: after commit, once
        })
    })
    return toInvoice(out), err
}
```

Why that is safe without a lock: two concurrent payments both read the same
balance, and under SERIALIZABLE postgres detects that the two transactions
cannot be ordered one after the other. It aborts one with `40001`; `RunTx`
re-runs that one from the top, where it reads the **committed** balance and
takes the branch that is now correct. A lock helper protects only the code
paths that remember to call it; serializable isolation protects every
read-check-write in the transaction, including the next one someone adds.

Assign whatever the RPC returns from inside `fn` (as `out` above) so the value
comes from the attempt that committed.

## A transaction that outlives one call: `BeginDetached`

`RunTx` is closure-scoped: the transaction ends when `fn` returns. When a unit
of work needs a transaction held across returns — a transaction-scoped
advisory lock taken by one function and released by another — open it with
`BeginDetached` on the same injected `DB orm.Context`:

```go
func (s *svc) LockRun(ctx context.Context, key int64) (release func(), err error) {
    if orm.InTx(ctx) { // a second connection would deadlock on the caller's locks
        return nil, svcerr.FailedPrecondition("LockRun needs its own transaction")
    }
    // READ COMMITTED: the advisory lock is the correctness argument.
    tx, err := s.deps.DB.BeginDetached(ctx, orm.ReadCommitted)
    if err != nil {
        return nil, err
    }
    if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
        _ = tx.Rollback()
        return nil, err
    }
    if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", key); err != nil {
        _ = tx.Rollback()
        return nil, err
    }
    return func() { _ = tx.Rollback() }, nil
}
```

- It **never joins** the transaction `ctx` carries: it is always a new
  transaction on its own connection, so inside a caller's `RunTx` its commit
  survives the caller's rollback. `orm.InTx(ctx)` tells you whether you are
  inside one.
- **You end it**: every path calls `Commit` or `Rollback`. Its lifetime is
  also bounded by `ctx` — cancelling `ctx` rolls it back (pass
  `context.WithoutCancel(ctx)` to outlive the request).
- **No retry**, so the options must say one attempt: `orm.ReadCommitted`, or
  `orm.Serializable.WithMaxAttempts(1)`. `orm.Serializable` itself is
  rejected — it promises retries nobody performs. A `40001` comes back to you.
- `tx.Join(ctx, fn)` hands `fn` a `ctx` carrying it (the transaction keeps
  the level it was begun at), so stores and generated delegates join it;
  `orm.AfterCommit` callbacks registered there run on `Commit`.
- It is the only way to begin a caller-owned transaction: `Client.BeginTx`,
  which took `*sql.TxOptions` and silently inherited the database default, is
  gone.

## A write that must survive the caller's rollback: `orm.Detached`

Inside `RunTx` every statement joins the caller's transaction. An audit row,
a write or idempotency ledger, an attempt counter must not: it records that
the attempt happened, so it has to commit even when the work rolls back.
`orm.Detached(ctx)` returns a `ctx` whose calls run on the **pool**, each its
own autocommit statement, outside every transaction `ctx` carries:

```go
err := s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
    // Committed now, kept if the transaction below rolls back or retries.
    if err := db.CreateLedgerEntry(orm.Detached(ctx), s.deps.DB, entry); err != nil {
        return err
    }
    return db.UpdateJobMasked(ctx, s.deps.DB, job, []string{"status"})
})
```

- Everything that resolves the carried transaction honours it: stores,
  generated delegates, `pkg/crud`, `db.Bun()`, the raw `Exec`/`Query`,
  `SQLConn`/`DBTX`, `orm.InTx` and `client.InTx` (both false). An explicit
  `*Tx` handle is unaffected.
- `s.deps.DB.RunTx(orm.Detached(ctx), opts, fn)` is a detached *transaction*:
  a new one, carried in the `ctx` fn receives as usual.
- Under a retrying `RunTx` it runs **once per attempt** — a retried
  transaction writes it again. Make the write idempotent, or use
  `orm.AfterCommit` when it should exist only if the work committed.
- Never for anything that must roll back with the caller, never to read the
  caller's uncommitted rows (it sees only committed ones), and never against a
  row the caller has written or locked: the detached statement waits on that
  lock while the caller waits on it, a deadlock postgres cannot see.

`BeginDetached` is the same "never joins" rule for a transaction you hold
open across calls; `Detached` is for a statement that needs no transaction of
its own.

## `RunTransaction` and `WithTx`

`RunTransaction(ctx, opts, func(tx orm.Context) error)` is the older,
handle-passing API, and pairs with the stores' `WithTx(tx)`. Its options mean
exactly what they mean to `RunTx` (`orm.ReadCommitted` runs `fn` once,
`orm.Serializable` retries it). Its `fn` receives a handle, not a
transaction-carrying `ctx`, so code called with the outer `ctx` and the plain
client does NOT participate — use the handle, or `tx.Join(ctx, fn)` to
get such a `ctx`. It joins a `RunTx` transaction already in `ctx`. Write new
code with `RunTx`.

## sqlc and hand SQL in the same transaction

A project porting onto the ORM keeps sqlc or database/sql code on the same
pool for a while, and one transaction then holds statements from both. Give
that code an executor that resolves the transaction from `ctx`, or it runs
on the pool, outside the transaction (and blocks forever on a row the
transaction has locked):

```go
queries := pgdb.New(client.DBTX()) // sqlc: every call joins ctx's RunTx

err := client.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
    if err := queries.InsertAuditRow(ctx, row); err != nil { // sqlc, in the tx
        return err
    }
    return db.UpdateJobMasked(ctx, client, job, []string{"status"}) // ORM, same tx
})
```

- `client.DBTX()` resolves on every call; `client.SQLConn(ctx)` is the
  executor for one statement (`*sql.Tx` inside a transaction, else the
  `*sql.DB`). Both satisfy sqlc's `DBTX`.
- `client.InTx(ctx)` asks about THIS pool; `orm.InTx(ctx)` about the
  innermost transaction on any. A layer deciding whether to join or begin
  uses the first.
- A layer that owns its own transaction loop (its own retry policy) opens
  each attempt with `client.BeginDetached(ctx, opts.WithMaxAttempts(1))`, runs
  its work under `tx.Join(ctx, fn)`, and ends it with `tx.Commit()` /
  `tx.Rollback()`: the ORM and `DBTX` both join it, and `orm.AfterCommit`
  callbacks run on its `Commit`.

## Porting a transaction: keep its level

A port from hand SQL, sqlc or `RunTransaction` onto `RunTx` keeps the level
the code was written for. Lock-based code (`SELECT … FOR UPDATE`, an advisory
lock) was correct at READ COMMITTED and stays there: `orm.ReadCommitted`.
Moving it to `orm.Serializable` silently changes its argument — the lock now
fights the snapshot, and under contention writers fail `40001` instead of
waiting their turn.

To drop the lock instead — usually the better end state — convert the whole
transaction to the lock-free recipe: a `Lock<Entity>(ctx, tx, id)` that runs
`SELECT … FOR UPDATE` becomes the generated `Get<Entity>ByID(ctx, s.deps.DB, id)`
(or the store's method) called with `RunTx`'s `ctx`; the
`RunTransaction(ctx, orm.ReadCommitted, func(tx orm.Context) …)` around it
becomes `s.deps.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) …)`,
and every `tx` argument inside becomes `s.deps.DB`. Delete the helper. Move
any side effect inside the closure into `orm.AfterCommit`.

Upgrading from a forge with default levels: `forge fix tx-isolation` spells
every old call out exactly as it ran (`RunTx(ctx, fn)` →
`orm.Serializable`; `RunTransaction(ctx, fn)` and `BeginTx(ctx, nil)` →
`orm.ReadCommitted`) and lists each site; then review the
`forgeconv-tx-lock-under-serializable` findings. See
`migrations/v0.1.47/tx-isolation`.

See also: `service-layer` for the Deps/store shape, `db/crud-overrides` for
read-check-write inside a CRUD op override.
