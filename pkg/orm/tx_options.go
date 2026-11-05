package orm

import (
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"strings"
)

// Isolation is a transaction's isolation level, and the policy every opener
// takes: RunTx(ctx, orm.Serializable, fn), BeginDetached(ctx,
// orm.ReadCommitted). Each level carries the attempt budget it needs —
// Serializable and RepeatableRead are retried on 40001 / 40P01 up to
// DefaultTxMaxAttempts, ReadCommitted runs once — and WithMaxAttempts /
// WithReadOnly refine it into a TxOptions.
//
// It has no default. The zero value is UNSET, and every opener rejects it:
// SERIALIZABLE and READ COMMITTED make opposite correctness arguments (the
// database proves serializability, versus a lock or a single statement does),
// so whichever one a blank value silently meant would be wrong for the code
// written for the other.
type Isolation int

const (
	// Serializable is SERIALIZABLE, retried on 40001 / 40P01 up to
	// DefaultTxMaxAttempts. Correct for read-check-write with NO locks: the
	// database aborts the loser of a race and the retry re-reads committed
	// state. fn may run more than once. Do not take a lock inside it
	// (forgeconv-tx-lock-under-serializable): the snapshot predates the lock
	// wait, so the waiter wakes into stale data and fails 40001.
	Serializable Isolation = iota + 1

	// RepeatableRead is REPEATABLE READ (snapshot isolation), retried on
	// 40001 / 40P01 up to DefaultTxMaxAttempts. Every statement sees the
	// snapshot taken at the first one; a write to a row changed since fails
	// with 40001. It does NOT detect write skew — two transactions each
	// reading what the other writes — which SERIALIZABLE does.
	RepeatableRead

	// ReadCommitted is READ COMMITTED, one attempt. Each STATEMENT sees the
	// rows committed before it began, so read-then-write is protected from a
	// lost update only by a lock it takes (SELECT … FOR UPDATE,
	// pg_advisory_xact_lock) — or by being one statement. It takes no
	// predicate locks and never raises 40001, so it has nothing to retry
	// unless it can deadlock; then say so with WithMaxAttempts.
	ReadCommitted
)

// DefaultTxMaxAttempts is the attempt budget Serializable and RepeatableRead
// carry: one try plus six retries.
//
// Sized from reliant's measurement on a single hot row under SERIALIZABLE
// (5 writes per writer): with 3 retries the failure rate was 0% at 4
// concurrent writers, 5% at 8 and 8.8% at 16; at 32 writers every write
// completed within one further attempt. Six retries covers 32-way contention
// with margin, and because a transaction that does not conflict never sleeps,
// the extra headroom costs nothing on the uncontended path.
const DefaultTxMaxAttempts = 7

// TxPolicy is what a transaction opener takes: an Isolation constant, or a
// TxOptions refined from one. Only this package implements it.
type TxPolicy interface {
	txOptions() TxOptions
}

func (i Isolation) txOptions() TxOptions {
	return TxOptions{Isolation: i, MaxAttempts: i.attempts()}
}

// attempts is the budget the level carries; 0 for an unset or unknown one.
func (i Isolation) attempts() int {
	switch i {
	case Serializable, RepeatableRead:
		return DefaultTxMaxAttempts
	case ReadCommitted:
		return 1
	}
	return 0
}

// WithMaxAttempts returns the level with the attempt budget n (1 = no retry).
func (i Isolation) WithMaxAttempts(n int) TxOptions { return i.txOptions().WithMaxAttempts(n) }

// WithReadOnly returns the level as a READ ONLY transaction (DEFERRABLE under
// SERIALIZABLE; see TxOptions.ReadOnly).
func (i Isolation) WithReadOnly() TxOptions { return i.txOptions().WithReadOnly() }

// sqlLevel maps an Isolation onto database/sql's constants. Only a validated
// level reaches it.
func (i Isolation) sqlLevel() sql.IsolationLevel {
	switch i {
	case ReadCommitted:
		return sql.LevelReadCommitted
	case RepeatableRead:
		return sql.LevelRepeatableRead
	case Serializable:
		return sql.LevelSerializable
	}
	panic(fmt.Sprintf("orm: unvalidated isolation %d", int(i)))
}

// String names the isolation level as postgres spells it.
func (i Isolation) String() string {
	switch i {
	case ReadCommitted:
		return "read committed"
	case RepeatableRead:
		return "repeatable read"
	case Serializable:
		return "serializable"
	case 0:
		return "unset"
	}
	return fmt.Sprintf("Isolation(%d)", int(i))
}

// TxOptions is a transaction's full policy: its isolation level, its access
// mode and how many times it may run. Derive one from a level rather than
// writing the struct out:
//
//	db.RunTx(ctx, orm.Serializable, fn)                     // retried up to DefaultTxMaxAttempts
//	db.RunTx(ctx, orm.ReadCommitted, fn)                    // one attempt
//	db.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(3), fn) // retried on deadlock (40P01)
//	tx, err := db.BeginDetached(ctx, orm.Serializable.WithReadOnly().WithMaxAttempts(1))
//
// Every field that decides behaviour must be set: the zero value is rejected.
// The level and the retry budget are stated together on purpose: retry is
// what makes SERIALIZABLE correct (an aborted transaction that is not re-run
// is a failed request), and what READ COMMITTED needs almost never.
type TxOptions struct {
	// Isolation selects the isolation level. Required.
	Isolation Isolation

	// ReadOnly runs the transaction READ ONLY, and under SERIALIZABLE also
	// DEFERRABLE.
	//
	// That second half is the point. A SERIALIZABLE read takes predicate
	// locks over whatever it touched (the whole relation, for a read no index
	// serves), and those locks are what make two writers on unrelated rows
	// abort each other. A SERIALIZABLE READ ONLY DEFERRABLE transaction takes
	// NO predicate locks and can never fail with 40001: it waits at its first
	// statement for a snapshot guaranteed free of later conflicts, then runs
	// outside the serialization graph entirely — neither suffering conflicts
	// nor causing them for concurrent writers.
	//
	// The cost is that wait, which can briefly delay a read on a busy
	// database. A write inside a read-only transaction is rejected by
	// postgres outright, so a mislabelled path fails loudly.
	ReadOnly bool

	// MaxAttempts bounds how many times fn runs: 1 is no retry, N re-runs fn
	// from the top on 40001 / 40P01 until N attempts have failed. Required
	// (at least 1). BeginDetached hands the transaction to the caller, so it
	// cannot retry and requires exactly 1.
	MaxAttempts int
}

func (o TxOptions) txOptions() TxOptions { return o }

// WithMaxAttempts returns o with the attempt budget n (1 = no retry).
func (o TxOptions) WithMaxAttempts(n int) TxOptions {
	o.MaxAttempts = n
	return o
}

// WithReadOnly returns o as a READ ONLY transaction (DEFERRABLE under
// SERIALIZABLE; see TxOptions.ReadOnly).
func (o TxOptions) WithReadOnly() TxOptions {
	o.ReadOnly = true
	return o
}

// String describes the policy: "serializable, 7 attempts".
func (o TxOptions) String() string {
	var b strings.Builder
	b.WriteString(o.Isolation.String())
	if o.ReadOnly {
		b.WriteString(" read only")
		if o.Isolation == Serializable {
			b.WriteString(" deferrable")
		}
	}
	if o.MaxAttempts == 1 {
		b.WriteString(", 1 attempt")
	} else {
		fmt.Fprintf(&b, ", %d attempts", o.MaxAttempts)
	}
	return b.String()
}

// ErrInvalidTxOptions is returned by a transaction opener handed a policy
// that does not say what it means: none at all, an unset (or unknown)
// isolation level, an unset attempt budget, or a retry budget on an opener
// that cannot retry. The error names the call site. Nothing is begun.
var ErrInvalidTxOptions = errors.New("orm: invalid transaction options")

// resolveTxPolicy turns the policy an opener was handed into validated
// options. op names the opener ("RunTx", "BeginDetached", …) for the error;
// detached is true for an opener that hands the transaction to the caller and
// so cannot retry.
func resolveTxPolicy(p TxPolicy, op string, detached bool) (TxOptions, error) {
	return resolveTxPolicyFrom(p, op, detached, 0)
}

// ResolveTxPolicy validates the policy a project's OWN transaction opener was
// handed — a func that takes an orm.TxPolicy and runs the transaction itself,
// such as a repository wrapper with a retry ladder of its own — and returns
// it as full options: level, access mode and attempt budget. It refuses what
// RunTx refuses (no policy, an unset or unknown level, no attempt budget), so
// the wrapper's callers name a level exactly as RunTx's do.
//
// op names the opener for the error ("Repo.RunTx"), and the error names the
// line that called the opener — one frame above the caller of
// ResolveTxPolicy. Declare the opener with `//forge:tx-opener level-arg=<n>`
// so forgeconv-tx-lock-under-serializable judges its calls as it judges
// RunTx's.
func ResolveTxPolicy(op string, p TxPolicy) (TxOptions, error) {
	return resolveTxPolicyFrom(p, op, false, 1)
}

// ResolveDetachedTxPolicy is ResolveTxPolicy for an opener that hands the
// transaction to its caller, as BeginDetached does: such an opener cannot
// re-run the caller's work, so it also requires exactly one attempt.
func ResolveDetachedTxPolicy(op string, p TxPolicy) (TxOptions, error) {
	return resolveTxPolicyFrom(p, op, true, 1)
}

// resolveTxPolicyFrom is resolveTxPolicy whose error names the call site
// skip frames further out than the first one outside this package.
func resolveTxPolicyFrom(p TxPolicy, op string, detached bool, skip int) (TxOptions, error) {
	if p == nil {
		return TxOptions{}, fmt.Errorf("%w: %s at %s passes no policy — pass orm.Serializable or orm.ReadCommitted (see orm.Isolation)", ErrInvalidTxOptions, op, callSite(skip))
	}
	o := p.txOptions()
	var problem string
	switch {
	case o.Isolation == 0:
		problem = "names no isolation level — pass orm.Serializable or orm.ReadCommitted (see orm.Isolation)"
	case o.Isolation < Serializable || o.Isolation > ReadCommitted:
		problem = fmt.Sprintf("names an unknown isolation level %d", int(o.Isolation))
	case o.MaxAttempts < 1:
		problem = fmt.Sprintf("sets no attempt budget (MaxAttempts %d) — use orm.%s, or orm.%s.WithMaxAttempts(n) with n ≥ 1", o.MaxAttempts, levelName(o.Isolation), levelName(o.Isolation))
	case detached && o.MaxAttempts != 1:
		problem = fmt.Sprintf("asks for %d attempts, but the caller owns this transaction and %s cannot retry it — pass orm.%s.WithMaxAttempts(1)", o.MaxAttempts, op, levelName(o.Isolation))
	default:
		return o, nil
	}
	return TxOptions{}, fmt.Errorf("%w: %s at %s %s", ErrInvalidTxOptions, op, callSite(skip), problem)
}

// levelName is the constant that spells level.
func levelName(level Isolation) string {
	switch level {
	case ReadCommitted:
		return "ReadCommitted"
	case RepeatableRead:
		return "RepeatableRead"
	}
	return "Serializable"
}

// ormPkg is this package's import path plus ".", the prefix of every function
// it declares, for callSite. A frame is placed by its function, not its file:
// under -trimpath a file is a module-relative path, and the package's
// identity should not depend on where its source sat at build time.
var ormPkg = func() string {
	pc, _, _, _ := runtime.Caller(0)
	name := runtime.FuncForPC(pc).Name() // <import path>.glob..func1
	slash := strings.LastIndex(name, "/")
	return name[:slash+strings.Index(name[slash:], ".")+1]
}()

// callSite returns file:line of the first caller outside this package's
// non-test sources — the line that opened the transaction — or, with skip
// > 0, of the caller that many frames further out (the line that called a
// project's own opener, for ResolveTxPolicy).
func callSite(skip int) string {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if !strings.HasPrefix(f.Function, ormPkg) || strings.HasSuffix(f.File, "_test.go") {
			if skip == 0 {
				return fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			skip--
		}
		if !more {
			return "an unknown call site"
		}
	}
}
