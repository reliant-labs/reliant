package identcollision

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestEveryArgumentReachesTheMock calls each method through the generated
// decorator into the generated mock, and compares what the mock recorded
// with what the caller passed. Compiling is not enough: a parameter shadowed
// by one of the wrapper's own locals can still type-check, and then forwards
// the local instead.
func TestEveryArgumentReachesTheMock(t *testing.T) {
	mock := &MockService{}
	svc := NewServiceWithForgeMiddleware(mock)
	ctx := context.Background()
	at := time.Unix(1700000000, 0)
	boom := errors.New("boom")

	_ = svc.Apply(ctx, "m", 1)
	_, _, _ = svc.Lookup("o", 2, true, "err", "context")
	_, _, _ = svc.Report(boom)
	_, _ = svc.Wait(ctx, 3*time.Second)
	_, _ = svc.Stamp("new", 4, "error")
	_, _ = svc.Fetch(ctx, "Result")
	_ = svc.Trace("ctx", at, "span", 5, 6, true)
	_ = svc.Mixed(ctx, "p0")
	_, _, _ = svc.Named(ctx, "m")
	svc.Fire(7, 8)
	_, _ = svc.Pair(9, "r0")

	for method, want := range map[string][]any{
		"Apply":  {"m", 1},
		"Lookup": {"o", 2, true, "err", "context"},
		"Report": {boom},
		"Wait":   {3 * time.Second},
		"Stamp":  {"new", 4, "error"},
		"Fetch":  {"Result"},
		"Trace":  {"ctx", at, "span", 5, 6, true},
		"Mixed":  {"p0"},
		"Named":  {"m"},
		"Fire":   {7, 8},
		"Pair":   {9, "r0"},
	} {
		calls := mock.Calls(method)
		if len(calls) != 1 {
			t.Errorf("%s: the mock recorded %d calls, want 1", method, len(calls))
			continue
		}
		if got := withoutContexts(calls[0].Args); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the mock received %#v, want %#v", method, got, want)
		}
	}
}

// withoutContexts drops context arguments, which the decorator's chain may
// legitimately replace with a derived context.
func withoutContexts(args []any) []any {
	var out []any
	for _, a := range args {
		if _, ok := a.(context.Context); ok {
			continue
		}
		out = append(out, a)
	}
	return out
}
