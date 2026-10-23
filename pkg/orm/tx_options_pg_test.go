package orm

import (
	"context"
	"testing"

	"github.com/lib/pq"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Each explicit level begins at that level, through every opener, as the
// server reports it from inside the transaction — the wire truth, not an
// options struct read back.
func TestTxOptions_EachOpenerBeginsAtItsLevel(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()

	type wire struct{ isolation, readOnly, deferrable string }
	levels := []struct {
		opts TxPolicy
		want wire
	}{
		{Serializable, wire{"serializable", "off", "off"}},
		{RepeatableRead, wire{"repeatable read", "off", "off"}},
		{ReadCommitted, wire{"read committed", "off", "off"}},
		{Serializable.WithReadOnly(), wire{"serializable", "on", "on"}},
		{ReadCommitted.WithReadOnly(), wire{"read committed", "on", "off"}},
	}
	probe := func(t *testing.T, ctx context.Context, q Context, want wire) {
		t.Helper()
		for name, w := range map[string]string{
			"transaction_isolation":  want.isolation,
			"transaction_read_only":  want.readOnly,
			"transaction_deferrable": want.deferrable,
		} {
			var got string
			if err := q.QueryRow(ctx, "SHOW "+name).Scan(&got); err != nil {
				t.Fatalf("SHOW %s: %v", name, err)
			}
			if got != w {
				t.Errorf("%s = %q, want %q", name, got, w)
			}
		}
	}
	for _, l := range levels {
		t.Run(l.opts.txOptions().String(), func(t *testing.T) {
			t.Run("RunTx", func(t *testing.T) {
				if err := c.RunTx(ctx, l.opts, func(ctx context.Context) error {
					probe(t, ctx, c, l.want)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
			t.Run("RunTransaction", func(t *testing.T) {
				if err := c.RunTransaction(ctx, l.opts, func(tx Context) error {
					probe(t, ctx, tx, l.want)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
			t.Run("BeginDetached", func(t *testing.T) {
				tx, err := c.BeginDetached(ctx, l.opts.txOptions().WithMaxAttempts(1))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				probe(t, ctx, tx, l.want)
			})
		})
	}
}

// The attempt budget is the policy's, on both closure openers: READ
// COMMITTED runs once, SERIALIZABLE re-runs a 40001 up to
// DefaultTxMaxAttempts, and an explicit budget is honoured.
func TestTxOptions_AttemptsFollowThePolicy(t *testing.T) {
	c := newTxTestClient(t)
	ctx := context.Background()
	conflict := &pq.Error{Code: "40001", Message: "could not serialize access"}

	cases := []struct {
		opts TxPolicy
		want int
	}{
		{ReadCommitted, 1},
		{ReadCommitted.WithMaxAttempts(2), 2},
		{Serializable, DefaultTxMaxAttempts},
		{Serializable.WithMaxAttempts(1), 1},
	}
	for _, tc := range cases {
		t.Run(tc.opts.txOptions().String(), func(t *testing.T) {
			for name, run := range map[string]func(func() error) error{
				"RunTx": func(f func() error) error {
					return c.RunTx(ctx, tc.opts, func(context.Context) error { return f() })
				},
				"RunTransaction": func(f func() error) error {
					return c.RunTransaction(ctx, tc.opts, func(Context) error { return f() })
				},
			} {
				attempts := 0
				err := run(func() error { attempts++; return conflict })
				if attempts != tc.want {
					t.Errorf("%s ran fn %d times, want %d", name, attempts, tc.want)
				}
				if !svcerr.IsAborted(err) {
					t.Errorf("%s: exhausted budget = %v, want svcerr Aborted", name, err)
				}
			}
		})
	}
}
