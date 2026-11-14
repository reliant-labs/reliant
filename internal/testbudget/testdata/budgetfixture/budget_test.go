// Package budgetfixture is a real suite for the test-budget guard to judge:
// testdata/short.jsonl is its `go test -short -json` output.
package budgetfixture

import (
	"testing"
	"time"
)

func TestFast(t *testing.T) {}

// TestSlowUnmarked takes longer than the 2s budget and is not marked slow:
// the defect the guard exists to report.
func TestSlowUnmarked(t *testing.T) {
	time.Sleep(2500 * time.Millisecond)
}

// TestMarkedSlow skips under -short, so a -short run never times it.
func TestMarkedSlow(t *testing.T) {
	if testing.Short() {
		t.Skip("sleeps 3s")
	}
	time.Sleep(3 * time.Second)
}

// TestTable's cases are each under budget; together they are not, and the
// whole table is what sits in the blocking tier.
func TestTable(t *testing.T) {
	for _, name := range []string{"a", "b", "c"} {
		t.Run(name, func(t *testing.T) { time.Sleep(800 * time.Millisecond) })
	}
}
