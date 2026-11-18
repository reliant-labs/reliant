---
name: migration-v1.0.0-tx-isolation
description: Upgrade v1.0.0 pkg/orm transactions to an explicit isolation level — run forge fix tx-isolation, clear what it lists, then review forgeconv-tx-lock-under-serializable.
relevance: migration
---

# v1.0.0 — explicit transaction isolation: no default level

Use when Go code calls `RunTx(ctx, fn)`, `RunTxWithOptions`,
`RunTransaction(ctx, fn)`, `RunTransactionWithOptions`, or an `*orm.Client`'s
`BeginTx`.

### What changed

pkg/orm had two opposite defaults. `RunTx`, `RunTxWithOptions` and
`BeginDetached` with an unset level began **SERIALIZABLE** (RunTx retrying
`40001`/`40P01` up to seven times); `Client.BeginTx` and `RunTransaction`
with nil options began at the database's default, **READ COMMITTED**, once.
The two make opposite correctness arguments — SERIALIZABLE is correct
without locks because the database aborts the loser and the retry re-reads;
READ COMMITTED is correct only with a lock or a single statement — so a port
from one call to the other changed what the code relied on, with no diff
line saying so. Lock-based code moved onto `RunTx` dropped updates under
contention (the waiter wakes into a stale snapshot and fails `40001`), and
lock-free code moved onto `RunTransaction` would double-apply.

Now every opener takes an `orm.TxPolicy` — a level constant
(`orm.Serializable`, `orm.ReadCommitted`, `orm.RepeatableRead`) carrying its
attempt budget, or an `orm.TxOptions` refined from one — and an unset level is
rejected with `orm.ErrInvalidTxOptions` naming the call site:

| Before | After |
|---|---|
| `RunTx(ctx, fn)` | `RunTx(ctx, orm.Serializable, fn)` — serializable, 7 attempts |
| `RunTxWithOptions(ctx, opts, fn)` | `RunTx(ctx, opts, fn)` |
| `tx.RunTx(ctx, fn)` on an `*orm.Tx` | `tx.Join(ctx, fn)` — a join keeps the transaction's own level |
| `RunTransaction(ctx, fn)` | `RunTransaction(ctx, orm.ReadCommitted, fn)` — read committed, 1 attempt |
| `RunTransactionWithOptions(ctx, nil, fn)` | `RunTransaction(ctx, orm.ReadCommitted, fn)` |
| `client.BeginTx(ctx, nil)` | `client.BeginDetached(ctx, orm.ReadCommitted)` |
| `BeginDetached(ctx, orm.TxOptions{})` | `BeginDetached(ctx, orm.Serializable.WithMaxAttempts(1))` |
| `orm.TxOptions{Isolation: orm.IsolationReadCommitted}` (7 attempts) | `orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts)` |
| `orm.IsolationSerializable` / `IsolationReadCommitted` / `IsolationRepeatableRead` | `orm.Serializable` / `ReadCommitted` / `RepeatableRead` |
| `orm.IsolationDefault` | gone — `orm.Serializable` |

`RunTxReadOnly(ctx, fn)` is unchanged (SERIALIZABLE READ ONLY DEFERRABLE): a
transaction that cannot write has one right level. `BeginDetached` (and the
`Tx` it returns) cannot retry, so it requires `MaxAttempts == 1`.
`RunTransaction` now honours the attempt budget like `RunTx`, and reports an
exhausted `40001`/`40P01` as `svcerr.Aborted` (the driver error kept as its
cause). An `orm.Context` implementation — a test double — needs the new
method set: `RunTx(ctx, policy orm.TxPolicy, fn)`, `RunTransaction(ctx, policy, fn)`,
`BeginDetached(ctx, policy)`, no `RunTxWithOptions`.

### What to do

1. Run the codemod (`forge project upgrade` runs it for you):

   ```bash
   forge fix tx-isolation --dry-run   # list every site
   forge fix tx-isolation
   ```

   It rewrites every call on a pkg/orm handle to the explicit form that
   **keeps today's behaviour exactly** — same level on the wire, same number
   of attempts — and never chooses a different one. It is type-aware: a
   project's own `RunTx` (a repository wrapper) and database/sql's `BeginTx`
   are left alone. It prints each site with the level it kept.

2. Clear what it lists under **Next**: options computed at run time (a
   `*sql.TxOptions` built from a variable, an `orm.TxOptions` whose level is
   not a constant) and `orm.Context` test doubles. Each line names the edit.

3. Review the levels it kept — this is the decision the codemod will not
   make. `forge lint --conventions` reports every SERIALIZABLE transaction
   whose body takes a lock as `forgeconv-tx-lock-under-serializable`:

   - the lock is the correctness argument (lock-based code that was
     READ COMMITTED before it was ported onto `RunTx`) → `orm.ReadCommitted`;
   - SERIALIZABLE is the argument → delete the lock.

   And the reverse, which no lint sees: a `RunTransaction` (now
   `orm.ReadCommitted`) whose closure reads, checks, then writes with no lock
   was racy before and is still; move it to `RunTx(ctx, orm.Serializable, …)`
   (`service-layer/transactions`, "Porting a transaction").

4. `go build ./... && go vet ./...`, then the tests that cover your
   transactions — a test that asserts a transaction's level on the wire
   (`SHOW transaction_isolation` inside it) is the durable guard.
