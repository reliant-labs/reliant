package orm

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// Every opener rejects options that leave a decision to a default, before it
// touches the database (the zero Client here has none), and names the line
// that asked.
func TestTxOptions_UnsetLevelIsRejectedNamingTheCallSite(t *testing.T) {
	ctx := context.Background()
	var c Client
	var tx Tx
	ran := false
	fn := func(context.Context) error { ran = true; return nil }
	handleFn := func(Context) error { ran = true; return nil }

	type opener struct {
		name string
		run  func(TxPolicy) (error, int)
	}
	line := func() int { _, _, l, _ := runtime.Caller(1); return l }
	openers := []opener{
		{"Client.RunTx", func(o TxPolicy) (error, int) { return c.RunTx(ctx, o, fn), line() }},
		{"Client.RunTransaction", func(o TxPolicy) (error, int) { return c.RunTransaction(ctx, o, handleFn), line() }},
		{"Client.BeginDetached", func(o TxPolicy) (error, int) { _, err := c.BeginDetached(ctx, o); return err, line() }},
		{"Tx.RunTx (a join)", func(o TxPolicy) (error, int) { return tx.RunTx(ctx, o, fn), line() }},
		{"Tx.RunTransaction (a join)", func(o TxPolicy) (error, int) { return tx.RunTransaction(ctx, o, handleFn), line() }},
	}
	// Built by mutation rather than as literals: `forge fix tx-isolation`
	// would (rightly, in old code) spell a level-less literal out.
	var zero TxOptions
	readOnlyNoLevel := ReadCommitted.WithReadOnly()
	readOnlyNoLevel.Isolation = 0
	unknown := ReadCommitted.WithMaxAttempts(1)
	unknown.Isolation = 9
	noBudget := ReadCommitted.WithMaxAttempts(0)
	bad := []struct {
		name string
		opts TxPolicy
		want string
	}{
		{"nil policy", nil, "passes no policy"},
		{"zero TxOptions", zero, "names no isolation level"},
		{"zero Isolation", Isolation(0), "names no isolation level"},
		{"read only, no level", readOnlyNoLevel, "names no isolation level"},
		{"unknown level", unknown, "unknown isolation level 9"},
		{"unknown bare level", Isolation(9), "unknown isolation level 9"},
		{"level without attempts", noBudget, "sets no attempt budget"},
		{"negative attempts", Serializable.WithMaxAttempts(-1), "sets no attempt budget"},
	}
	for _, op := range openers {
		for _, b := range bad {
			t.Run(op.name+"/"+b.name, func(t *testing.T) {
				ran = false
				err, at := op.run(b.opts)
				if !errors.Is(err, ErrInvalidTxOptions) {
					t.Fatalf("err = %v, want ErrInvalidTxOptions", err)
				}
				if !strings.Contains(err.Error(), b.want) {
					t.Errorf("err = %q, want it to say %q", err, b.want)
				}
				if site := fmt.Sprintf("tx_options_test.go:%d", at); !strings.Contains(err.Error(), site) {
					t.Errorf("err = %q, want it to name the call site %s", err, site)
				}
				if ran {
					t.Error("fn ran despite invalid options")
				}
			})
		}
	}

	// BeginDetached hands the transaction to the caller and cannot retry:
	// a budget above one is a promise nobody keeps.
	_, err := c.BeginDetached(ctx, Serializable)
	if !errors.Is(err, ErrInvalidTxOptions) || !strings.Contains(err.Error(), "orm.Serializable.WithMaxAttempts(1)") {
		t.Errorf("BeginDetached(Serializable) = %v, want ErrInvalidTxOptions naming orm.Serializable.WithMaxAttempts(1)", err)
	}
}

// The three levels carry the budgets the package documents, and the
// modifiers derive variants without touching them.
func TestTxOptions_LevelsAndModifiers(t *testing.T) {
	var zero TxOptions
	cases := []struct {
		opts TxOptions
		want string
	}{
		{Serializable.txOptions(), "serializable, 7 attempts"},
		{RepeatableRead.txOptions(), "repeatable read, 7 attempts"},
		{ReadCommitted.txOptions(), "read committed, 1 attempt"},
		{Serializable.WithMaxAttempts(1), "serializable, 1 attempt"},
		{Serializable.WithReadOnly(), "serializable read only deferrable, 7 attempts"},
		{ReadCommitted.WithReadOnly().WithMaxAttempts(3), "read committed read only, 3 attempts"},
		{zero, "unset, 0 attempts"},
	}
	for _, tc := range cases {
		if got := tc.opts.String(); got != tc.want {
			t.Errorf("%#v.String() = %q, want %q", tc.opts, got, tc.want)
		}
	}
	for _, p := range []TxPolicy{Serializable, RepeatableRead, ReadCommitted, Serializable.WithReadOnly(), TxOptions{Isolation: ReadCommitted, MaxAttempts: 2}} {
		if _, err := resolveTxPolicy(p, "RunTx", false); err != nil {
			t.Errorf("%v rejected: %v", p, err)
		}
	}
	if _, err := resolveTxPolicy(ReadCommitted, "BeginDetached", true); err != nil {
		t.Errorf("ReadCommitted rejected by BeginDetached: %v", err)
	}
}

// projectOpener is a project's own opener: it takes a policy and runs the
// transaction itself, so it resolves the policy with ResolveTxPolicy.
func projectOpener(p TxPolicy) (TxOptions, error) { return ResolveTxPolicy("Repo.RunTx", p) }

// projectDetachedOpener hands its transaction to the caller.
func projectDetachedOpener(p TxPolicy) (TxOptions, error) {
	return ResolveDetachedTxPolicy("Repo.BeginTx", p)
}

// A project's own opener resolves a policy to the options RunTx would run,
// refuses what RunTx refuses, and the refusal names the line that called the
// opener — not the opener's own call to ResolveTxPolicy.
func TestResolveTxPolicy_ForAProjectsOwnOpener(t *testing.T) {
	for _, tc := range []struct {
		p    TxPolicy
		want string
	}{
		{Serializable, "serializable, 7 attempts"},
		{Serializable.WithMaxAttempts(1), "serializable, 1 attempt"},
		{Serializable.WithReadOnly(), "serializable read only deferrable, 7 attempts"},
		{ReadCommitted, "read committed, 1 attempt"},
		{ReadCommitted.WithMaxAttempts(DefaultTxMaxAttempts), "read committed, 7 attempts"},
	} {
		got, err := projectOpener(tc.p)
		if err != nil {
			t.Fatalf("ResolveTxPolicy(%v): %v", tc.p, err)
		}
		if got.String() != tc.want {
			t.Errorf("ResolveTxPolicy(%v) = %q, want %q", tc.p, got, tc.want)
		}
	}

	_, err := projectOpener(Isolation(0)) // the line the error must name
	_, _, line, _ := runtime.Caller(0)
	if !errors.Is(err, ErrInvalidTxOptions) || !strings.Contains(err.Error(), "names no isolation level") {
		t.Fatalf("ResolveTxPolicy(unset) = %v, want ErrInvalidTxOptions naming no isolation level", err)
	}
	if site := fmt.Sprintf("tx_options_test.go:%d", line-1); !strings.Contains(err.Error(), "Repo.RunTx at ") || !strings.Contains(err.Error(), site) {
		t.Errorf("err = %q, want it to name Repo.RunTx and its caller %s", err, site)
	}

	if _, err := projectDetachedOpener(ReadCommitted); err != nil {
		t.Errorf("ResolveDetachedTxPolicy(ReadCommitted): %v", err)
	}
	if _, err := projectDetachedOpener(Serializable); !errors.Is(err, ErrInvalidTxOptions) || !strings.Contains(err.Error(), "Repo.BeginTx cannot retry") {
		t.Errorf("ResolveDetachedTxPolicy(Serializable) = %v, want a refusal of its 7 attempts", err)
	}
}
