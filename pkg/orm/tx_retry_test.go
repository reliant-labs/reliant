package orm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Both drivers' errors, wrapped as a service would wrap them: every
// classifier must see through %w and through RunTx's *TransactionError.
func wrappedPgErrors(code string) map[string]error {
	return map[string]error{
		"pgconn":      fmt.Errorf("load thing: %w", &pgconn.PgError{Code: code}),
		"pq":          fmt.Errorf("load thing: %w", &pq.Error{Code: pq.ErrorCode(code)}),
		"transaction": NewTransactionError("commit", &pgconn.PgError{Code: code}),
	}
}

// The default stays exactly 40001 / 40P01, and WithRetryOn only adds to it.
func TestTxOptions_RetryableIsTheDefaultOredWithClassifiers(t *testing.T) {
	plain := Serializable.txOptions()
	widened := Serializable.WithRetryOn(RetryOnSQLState("40002"))

	for _, code := range []string{"40001", "40P01"} {
		for name, err := range wrappedPgErrors(code) {
			if !plain.Retryable(err) || !widened.Retryable(err) {
				t.Errorf("%s %s: default classification lost (plain %v, widened %v)", name, code, plain.Retryable(err), widened.Retryable(err))
			}
		}
	}
	for _, code := range []string{"40002", "25P02", "23505"} {
		for name, err := range wrappedPgErrors(code) {
			if plain.Retryable(err) {
				t.Errorf("%s %s: retried by default; the default must stay 40001/40P01", name, code)
			}
			if got, want := widened.Retryable(err), code == "40002"; got != want {
				t.Errorf("%s %s: widened.Retryable = %v, want %v", name, code, got, want)
			}
		}
	}
	for _, err := range []error{nil, errors.New("boom"), NewTransactionError("commit", pgx.ErrTxCommitRollback)} {
		if plain.Retryable(err) {
			t.Errorf("plain.Retryable(%v) = true, want false", err)
		}
	}
	if widened.Retryable(nil) {
		t.Error("Retryable(nil) = true; a classifier must never be asked about success")
	}
}

func TestTxOptions_WithRetryOnAccumulatesWithoutAliasing(t *testing.T) {
	isA := func(err error) bool { return err != nil && err.Error() == "a" }
	isB := func(err error) bool { return err != nil && err.Error() == "b" }
	a, b := errors.New("a"), errors.New("b")

	base := Serializable.WithMaxAttempts(3)
	onA := base.WithRetryOn(isA)
	onB := base.WithRetryOn(isB) // derived from the same base: must not see isA
	onAB := onA.WithRetryOn(isB)

	for _, tc := range []struct {
		name         string
		opts         TxOptions
		wantA, wantB bool
	}{
		{"base", base, false, false},
		{"onA", onA, true, false},
		{"onB", onB, false, true},
		{"onAB", onAB, true, true},
		{"nil adds nothing", onA.WithRetryOn(nil), true, false},
		{"Isolation.WithRetryOn", ReadCommitted.WithRetryOn(isA), true, false},
	} {
		if got := tc.opts.Retryable(a); got != tc.wantA {
			t.Errorf("%s.Retryable(a) = %v, want %v", tc.name, got, tc.wantA)
		}
		if got := tc.opts.Retryable(b); got != tc.wantB {
			t.Errorf("%s.Retryable(b) = %v, want %v", tc.name, got, tc.wantB)
		}
	}
	if onA.MaxAttempts != 3 || onA.Isolation != Serializable || onA.WithMaxAttempts(5).Retryable(a) != true {
		t.Errorf("WithRetryOn must keep, and be kept by, the other modifiers: %v", onA)
	}

	// TxOptions stays comparable, and == on options carrying a classifier
	// neither panics nor confuses two different classifiers.
	if base != Serializable.WithMaxAttempts(3) {
		t.Error("options without a classifier must still compare equal")
	}
	same := onA
	if onA == onB || same != onA || onA.WithRetryOn(nil) != onA {
		t.Error("options with classifiers compare by the WithRetryOn result they carry")
	}
	_ = map[TxOptions]bool{onA: true} // usable as a map key: compiles and does not panic
}

func TestTxOptions_StringNamesTheClassifiers(t *testing.T) {
	isA := func(error) bool { return false }
	for _, tc := range []struct {
		opts TxOptions
		want string
	}{
		{Serializable.WithRetryOn(isA), "serializable, 7 attempts, +1 retry classifier"},
		{ReadCommitted.WithMaxAttempts(3).WithRetryOn(isA).WithRetryOn(isA), "read committed, 3 attempts, +2 retry classifiers"},
		{Serializable.WithRetryOn(nil), "serializable, 7 attempts"},
	} {
		if got := tc.opts.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}

// Every opener accepts a classifier — BeginDetached included, whose
// one-attempt rule is about the budget and which never consults it — so a
// project's opener can add its classifier to whatever policy it was handed.
func TestTxOptions_ClassifierIsAcceptedByEveryOpener(t *testing.T) {
	retry := RetryOnSQLState("40002")
	for _, p := range []TxPolicy{Serializable.WithRetryOn(retry), ReadCommitted.WithRetryOn(retry)} {
		if _, err := resolveTxPolicy(p, "RunTx", false); err != nil {
			t.Errorf("RunTx rejected %v: %v", p, err)
		}
		if got, err := projectOpener(p); err != nil || !got.Retryable(&pgconn.PgError{Code: "40002"}) {
			t.Errorf("ResolveTxPolicy(%v) = %v, %v; want the classifier carried through", p, got, err)
		}
	}
	if _, err := resolveTxPolicy(ReadCommitted.WithRetryOn(retry), "BeginDetached", true); err != nil {
		t.Errorf("BeginDetached rejected an inert classifier: %v", err)
	}
	if _, err := resolveTxPolicy(Serializable.WithRetryOn(retry), "BeginDetached", true); !errors.Is(err, ErrInvalidTxOptions) {
		t.Errorf("BeginDetached must still refuse the 7-attempt budget, got %v", err)
	}
}

// runAttempts consults the classifier, and MaxAttempts bounds it.
func TestRunAttempts_RetriesWhatTheClassifierReports(t *testing.T) {
	custom := &pgconn.PgError{Code: "40002", Message: "deferred constraint"}
	ctx := context.Background()

	t.Run("exhausts to Aborted keeping the cause", func(t *testing.T) {
		n := 0
		err := runAttempts(ctx, Serializable.WithMaxAttempts(2).WithRetryOn(RetryOnSQLState("40002")),
			func() error { n++; return fmt.Errorf("body: %w", custom) })
		if n != 2 {
			t.Errorf("attempt ran %d times, want MaxAttempts=2", n)
		}
		var pgErr *pgconn.PgError
		if !svcerr.IsAborted(err) || !errors.As(err, &pgErr) || pgErr.Code != "40002" {
			t.Errorf("exhausted = %v, want svcerr Aborted with the 40002 as cause", err)
		}
	})
	t.Run("one attempt preserves the exact error and never calls the custom classifier", func(t *testing.T) {
		n, classified := 0, 0
		policy := ReadCommitted.WithRetryOn(func(error) bool { classified++; return true })
		bodyErr := fmt.Errorf("body: %w", custom)
		if err := runAttempts(ctx, policy, func() error { n++; return bodyErr }); n != 1 || classified != 0 || err != bodyErr {
			t.Fatalf("attempts=%d classifier calls=%d err=%v; want one attempt, no classification, and identical error", n, classified, err)
		}
		for _, code := range []string{"40001", "40P01"} {
			conflict := &pgconn.PgError{Code: code}
			err := runAttempts(ctx, policy, func() error { return conflict })
			if !svcerr.IsAborted(err) || !errors.Is(err, conflict) || classified != 0 {
				t.Errorf("native conflict %s: err=%v classifier calls=%d; want Aborted with original cause and no custom classification", code, err, classified)
			}
		}
	})
	t.Run("an unclassified error is returned verbatim", func(t *testing.T) {
		n := 0
		err := runAttempts(ctx, Serializable.WithRetryOn(RetryOnSQLState("40002")),
			func() error { n++; return &pgconn.PgError{Code: "23505"} })
		if n != 1 || svcerr.IsAborted(err) {
			t.Errorf("attempts=%d err=%v; want 1 attempt and the 23505 itself", n, err)
		}
	})
}

func TestRetryOnSQLState(t *testing.T) {
	retry := RetryOnSQLState("40002", "25p02") // case-insensitive, as 40P01 is spelled both ways
	for _, code := range []string{"40002", "25P02"} {
		for name, err := range wrappedPgErrors(code) {
			if !retry(err) {
				t.Errorf("%s %s: not matched", name, code)
			}
		}
	}
	for _, err := range []error{nil, errors.New("sqlstate 40002"), &pgconn.PgError{Code: "40001"}, &pq.Error{Code: "23505"}} {
		if retry(err) {
			t.Errorf("matched %v", err)
		}
	}

	for _, codes := range [][]string{nil, {"4001"}, {"40002", "400022"}} {
		func() {
			defer func() {
				if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "orm.RetryOnSQLState") {
					t.Errorf("RetryOnSQLState(%q) did not panic naming itself: %v", codes, r)
				}
			}()
			RetryOnSQLState(codes...)
		}()
	}
}

func TestRetryOnCommitRollback(t *testing.T) {
	for _, err := range []error{
		pgx.ErrTxCommitRollback,
		NewTransactionError("commit", pgx.ErrTxCommitRollback), // what runTxAttempt returns
		fmt.Errorf("save: %w", NewTransactionError("commit", pq.ErrInFailedTransaction)),
	} {
		if !RetryOnCommitRollback(err) {
			t.Errorf("RetryOnCommitRollback(%v) = false", err)
		}
	}
	for _, err := range []error{nil, errors.New("commit unexpectedly resulted in rollback"), &pgconn.PgError{Code: "40001"}} {
		if RetryOnCommitRollback(err) {
			t.Errorf("RetryOnCommitRollback(%v) = true", err)
		}
	}
}
