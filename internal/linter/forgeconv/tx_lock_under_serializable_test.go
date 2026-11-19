package forgeconv

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/suppress"
)

const txLockSrc = `package store

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
)

const lockQuery = "SELECT id FROM jobs WHERE id = $1 FOR UPDATE"

func a(ctx context.Context, db orm.Context) error {
	return db.RunTx(ctx, orm.Serializable, func(ctx context.Context) error { // FOR UPDATE literal
		_, err := db.Exec(ctx, "SELECT 1 FROM jobs WHERE id = $1 FOR UPDATE", 1)
		return err
	})
}

func b(ctx context.Context, db orm.Context) error {
	return db.RunTx(ctx, orm.Serializable.WithMaxAttempts(3), func(ctx context.Context) error { // a const
		_, err := db.Exec(ctx, lockQuery, 1)
		return err
	})
}

func c(ctx context.Context, db orm.Context) error {
	return db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error { // RC: the lock is the point
		_, err := db.Exec(ctx, lockQuery, 1)
		return err
	})
}

func d(ctx context.Context, db orm.Context) error {
	tx, err := db.BeginDetached(ctx, orm.Serializable.WithMaxAttempts(1)) // advisory lock later
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", 7)
	return err
}

func e(ctx context.Context, db orm.Context) error {
	tx, err := db.BeginDetached(ctx, orm.ReadCommitted)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", 7)
	return err
}

func f(ctx context.Context, db orm.Context) error {
	return db.RunTransaction(ctx, orm.TxOptions{Isolation: orm.RepeatableRead, MaxAttempts: 2}, func(tx orm.Context) error { // LOCK TABLE
		_, err := tx.Exec(ctx, "LOCK TABLE jobs IN EXCLUSIVE MODE")
		return err
	})
}

func g(ctx context.Context, db orm.Context) error {
	return db.RunTx(ctx, orm.Serializable, lockedBody(db)) // not a func literal or a package func: not judged
}

func lockedBody(db orm.Context) func(context.Context) error { return nil }

func h(ctx context.Context, db orm.Context) error {
	return db.RunTx(ctx, orm.Serializable, bunLocked) // package func with .For("UPDATE")
}

func bunLocked(ctx context.Context) error {
	var q interface{ For(string) any }
	q.For("UPDATE")
	return nil
}

func i(ctx context.Context, db orm.Context) error {
	return db.RunTx(ctx, orm.Serializable, func(ctx context.Context) error { // no lock
		_, err := db.Exec(ctx, "UPDATE jobs SET state = 'done' WHERE id = $1", 1)
		return err
	})
}

func j(ctx context.Context, db orm.Context) error {
	return db.RunTx(ctx, orm.Serializable.WithReadOnly(), func(ctx context.Context) error { // read only: not judged
		_, err := db.Exec(ctx, lockQuery, 1)
		return err
	})
}

func k(ctx context.Context, db orm.Context) error {
	// forge:lint-disable-next-line forgeconv-tx-lock-under-serializable: the retry is the point here
	return db.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		_, err := db.Exec(ctx, lockQuery, 1)
		return err
	})
}
`

func TestLintTxLockUnderSerializable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "store.go"), []byte(txLockSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, strict := range []bool{false, true} {
		res, err := LintTxLockUnderSerializable(dir, LintOptions{Strict: strict})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range res.Findings {
			want := SeverityWarning
			if strict {
				want = SeverityError
			}
			if f.Severity != want {
				t.Errorf("strict=%v: %s:%d is %s, want %s", strict, f.File, f.Line, f.Severity, want)
			}
			if !strings.Contains(f.Remediation, "orm.ReadCommitted") {
				t.Errorf("remediation names no fix: %q", f.Remediation)
			}
			got = append(got, fmt.Sprintf("%s:%d %s", f.File, f.Line, between(f.Message, "— ", " at ")))
		}
		// k is found here and suppressed by the shared suppression pass
		// `forge lint` runs over every Go finding.
		want := []string{
			"store.go:12 SELECT … FOR UPDATE",
			"store.go:19 SELECT … FOR UPDATE",
			"store.go:33 pg_advisory_xact_lock",
			"store.go:53 LOCK TABLE",
			"store.go:66 .For(\"UPDATE\")",
			"store.go:91 SELECT … FOR UPDATE",
		}
		if !slices.Equal(got, want) {
			t.Errorf("strict=%v findings:\n  %s\nwant:\n  %s", strict, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
		applied := suppress.Apply(txLockSrc, res.Findings)
		if len(applied.Kept) != len(want)-1 || len(applied.Violations) != 0 {
			t.Errorf("strict=%v: the reasoned disable above k must suppress exactly its finding: kept %d, violations %v", strict, len(applied.Kept), applied.Violations)
		}
	}
}

func between(s, from, to string) string {
	_, after, ok := strings.Cut(s, from)
	if !ok {
		return s
	}
	before, _, _ := strings.Cut(after, to)
	return before
}

// A project's own openers, declared with //forge:tx-opener in one package
// and called from another.
const txOpenerDeclSrc = `package db

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
)

type Repo struct{}

type Tx struct{}

//forge:tx-opener level-arg=1
func (r *Repo) RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error {
	return nil
}

// BeginLocked hands the transaction to its caller: detached.
//
// forge:tx-opener level-arg=1
func (r *Repo) BeginLocked(ctx context.Context, policy orm.TxPolicy) (*Tx, error) { return nil, nil }

//forge:tx-opener level-arg=2 body-arg=3
func (r *Repo) Mutate(ctx context.Context, id string, policy orm.TxPolicy, fn, after func(ctx context.Context) error) error {
	return nil
}

//forge:tx-opener level-arg=1
func RunLocked(ctx context.Context, policy orm.TxPolicy, fn func(context.Context) error) error {
	return nil
}

//forge:tx-opener level-arg=1
func runLocal(ctx context.Context, policy orm.TxPolicy, fn func(context.Context) error) error { return nil }

func local(ctx context.Context) error {
	return runLocal(ctx, orm.Serializable, func(ctx context.Context) error { // a bare call to a declared func
		var db interface{ Exec(string) }
		db.Exec("SELECT pg_advisory_xact_lock(1)")
		return nil
	})
}

//forge:tx-opener level-arg=4
func (r *Repo) OutOfRange(ctx context.Context, policy orm.TxPolicy) error { return nil }

//forge:tx-opener
func (r *Repo) NoLevel(ctx context.Context, policy orm.TxPolicy) error { return nil }

//forge:tx-opener level-arg=1 body-arg=0
func (r *Repo) BodyNotFunc(ctx context.Context, policy orm.TxPolicy, fn func(context.Context) error) error {
	return nil
}

//forge:tx-opener level=1
func (r *Repo) UnknownKey(ctx context.Context, policy orm.TxPolicy) error { return nil }

//forge:tx-opener level-arg=1
type NotAFunc struct{}
`

const txOpenerCallSrc = `package svc

import (
	"context"

	"example.com/app/db"
	"github.com/reliant-labs/forge/pkg/orm"
)

type S struct{ repo *db.Repo }

func (s S) a(ctx context.Context) error {
	return s.repo.RunTx(ctx, orm.Serializable, func(ctx context.Context) error { // declared opener, FOR UPDATE
		return exec(ctx, "SELECT id FROM chats WHERE id = $1 FOR UPDATE")
	})
}

func (s S) b(ctx context.Context) error {
	return s.repo.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(7), func(ctx context.Context) error { // RC: fine
		return exec(ctx, "SELECT id FROM chats WHERE id = $1 FOR UPDATE")
	})
}

func (s S) c(ctx context.Context) error {
	tx, err := s.repo.BeginLocked(ctx, orm.Serializable.WithMaxAttempts(1)) // detached declared opener
	if err != nil {
		return err
	}
	_ = tx
	return exec(ctx, "SELECT pg_advisory_xact_lock(1)")
}

func (s S) d(ctx context.Context) error {
	return s.repo.Mutate(ctx, "id", orm.Serializable, func(ctx context.Context) error { // level-arg=2 body-arg=3
		return exec(ctx, "LOCK TABLE chats")
	}, func(ctx context.Context) error {
		return nil
	})
}

func (s S) e(ctx context.Context) error {
	return s.repo.Mutate(ctx, "id", orm.Serializable, func(ctx context.Context) error { // the lock is in the after func, not the body
		return nil
	}, func(ctx context.Context) error {
		return exec(ctx, "LOCK TABLE chats")
	})
}

func (s S) f(ctx context.Context) error {
	return db.RunLocked(ctx, orm.Serializable, func(ctx context.Context) error { // package func of another package
		return exec(ctx, "SELECT id FROM chats FOR NO KEY UPDATE")
	})
}

func (s S) g(ctx context.Context) error {
	return s.repo.RunTx(ctx, orm.Serializable, func(ctx context.Context) error { // no lock
		return exec(ctx, "UPDATE chats SET title = 'x'")
	})
}

func (s S) h(ctx context.Context, other interface {
	Mutate(context.Context, orm.TxPolicy, func(context.Context) error) error
}) error {
	return other.Mutate(ctx, orm.Serializable, func(ctx context.Context) error { // another arity: not the declared Mutate
		return exec(ctx, "LOCK TABLE chats")
	})
}

func (s S) i(ctx context.Context, db orm.Context) error {
	return db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		return s.repo.RunTx(ctx, orm.Serializable, func(ctx context.Context) error { // nested: judged on its own
			return exec(ctx, "SELECT 1 FOR UPDATE")
		})
	})
}

func exec(ctx context.Context, q string) error { return nil }
`

func TestLintTxLockUnderSerializable_ProjectDeclaredOpeners(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for path, src := range map[string]string{"db/repo.go": txOpenerDeclSrc, "svc/svc.go": txOpenerCallSrc} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := LintTxLockUnderSerializable(dir, LintOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range res.Findings {
		what := between(f.Message, "— ", " at ")
		if strings.HasPrefix(f.Message, "//forge:tx-opener") {
			what = f.Message[:strings.Index(f.Message, ", so forge ignores it")]
			if f.Severity != SeverityError {
				t.Errorf("%s:%d: a malformed marker is %s, want error", f.File, f.Line, f.Severity)
			}
		}
		got = append(got, fmt.Sprintf("%s:%d %s", f.File, f.Line, what))
	}
	want := []string{
		"db/repo.go:37 pg_advisory_xact_lock",
		"db/repo.go:44 //forge:tx-opener on OutOfRange names argument 4, but it takes 2",
		"db/repo.go:47 //forge:tx-opener on NoLevel names no level-arg",
		"db/repo.go:50 //forge:tx-opener on BodyNotFunc names argument 0 as the body, but it is not a func",
		"db/repo.go:55 //forge:tx-opener on UnknownKey has an unknown key \"level\" (level-arg, body-arg)",
		"db/repo.go:58 //forge:tx-opener is not on a func declaration",
		"svc/svc.go:13 SELECT … FOR UPDATE",
		"svc/svc.go:25 pg_advisory_xact_lock",
		"svc/svc.go:34 LOCK TABLE",
		"svc/svc.go:50 SELECT … FOR UPDATE",
		"svc/svc.go:71 SELECT … FOR UPDATE",
	}
	if !slices.Equal(got, want) {
		t.Errorf("findings:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
