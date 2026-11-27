// The tag keeps this file out of a plain `go test` of the fixture directory:
// it needs mock_gen.go, which exists only in the sandbox
// TestGenerate_TypedCallRecords generates it into, and that test builds it
// with -tags callrecords.

//go:build callrecords

package callrecords

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

type ctxKey struct{}

// TestTypedCallRecords drives every method of the generated mock and reads
// the arguments back through the typed accessors — by field name, with no
// type assertion in the test. It compiling at all pins the names and types
// the generator chose; the comparisons pin that each field holds the
// argument at its own position.
func TestTypedCallRecords(t *testing.T) {
	m := &MockService{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "caller")
	var b strings.Builder
	src := strings.NewReader("in")

	_ = m.Publish(ctx, Lease{JobID: "job-1", Epoch: 7}, 1)
	_ = m.Publish(ctx, Lease{JobID: "job-1", Epoch: 9}, 2)
	_ = m.Rename(ctx, "renamed")
	_ = m.Unnamed(ctx, "s", 3)
	_ = m.Blank("blank", 4)
	_ = m.Clash(5, "X")
	_ = m.Variadic("p", "a", "b")
	_ = m.Variadic("none")
	m.Fail(nil)
	_ = m.Ping()
	m.Foo(6)
	_ = m.FooCalls()
	_ = m.Taken("id-1")
	_ = m.Aliased(src, &b)

	publishes := m.PublishCalls()
	if len(publishes) != 2 {
		t.Fatalf("PublishCalls() = %d records, want 2", len(publishes))
	}
	if got := publishes[1]; got.Lease.Epoch != 9 || got.Version != 2 || got.Ctx.Value(ctxKey{}) != "caller" {
		t.Errorf("PublishCalls()[1] = %+v, want epoch 9, version 2 and the caller's context", got)
	}
	if got := m.RenameCalls(); len(got) != 1 || got[0].M != "renamed" {
		t.Errorf("RenameCalls() = %+v, want M=renamed", got)
	}
	if got := m.UnnamedCalls(); len(got) != 1 || got[0].P0 != ctx || got[0].P1 != "s" || got[0].P2 != 3 {
		t.Errorf("UnnamedCalls() = %+v, want P0=ctx P1=s P2=3", got)
	}
	if got := m.BlankCalls(); len(got) != 1 || got[0].P0_ != "blank" || got[0].P0 != 4 {
		t.Errorf("BlankCalls() = %+v, want P0_=blank (the blank) P0=4 (the named p0)", got)
	}
	if got := m.ClashCalls(); len(got) != 1 || got[0].X != 5 || got[0].X_ != "X" {
		t.Errorf("ClashCalls() = %+v, want X=5 X_=X", got)
	}
	variadic := m.VariadicCalls()
	if len(variadic) != 2 || variadic[0].Prefix != "p" || !reflect.DeepEqual(variadic[0].Rest, []string{"a", "b"}) || variadic[1].Rest != nil {
		t.Errorf("VariadicCalls() = %+v, want Rest=[a b] then a nil Rest", variadic)
	}
	if got := m.FailCalls(); len(got) != 1 || got[0].Err != nil {
		t.Errorf("FailCalls() = %+v, want one record with a nil Err", got)
	}
	if got := m.PingCalls(); len(got) != 1 {
		t.Errorf("PingCalls() = %d records, want 1", len(got))
	}
	// The contract's FooCalls keeps its name; Foo's accessor moved aside.
	if got := m.FooCalls_(); len(got) != 1 || got[0].N != 6 {
		t.Errorf("FooCalls_() = %+v, want N=6", got)
	}
	if got := m.FooCallsCalls(); len(got) != 1 {
		t.Errorf("FooCallsCalls() = %d records, want 1", len(got))
	}
	// The package's own MockServiceTakenCall is untouched; the record moved.
	var _ MockServiceTakenCall = struct{}{}
	if got := m.TakenCalls(); len(got) != 1 || got[0].ID != "id-1" {
		t.Errorf("TakenCalls() = %+v, want ID=id-1", got)
	}
	var _ []MockServiceTakenCall_ = m.TakenCalls()
	if got := m.AliasedCalls(); len(got) != 1 || got[0].Src != src || got[0].B != &b {
		t.Errorf("AliasedCalls() = %+v, want the caller's reader and builder", got)
	}

	// Recorder.Calls is unchanged: the typed accessor reads the same log.
	if calls := m.Calls("Publish"); len(calls) != 2 || calls[1].Args[1].(Lease).Epoch != 9 {
		t.Errorf("Calls(%q) = %+v, want the positional log unchanged", "Publish", calls)
	}
	// No calls is nil, like Recorder.Calls.
	m.Reset()
	if got := m.PublishCalls(); got != nil {
		t.Errorf("PublishCalls() after Reset = %+v, want nil", got)
	}
}
