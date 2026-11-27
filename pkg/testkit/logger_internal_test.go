package testkit

import (
	"fmt"
	"strings"
	"testing"
)

// cleanupTB records Cleanup and Log so a test can run a Logger's flush on
// demand and read what it wrote.
type cleanupTB struct {
	testing.TB
	failed   bool
	cleanups []func()
	logs     []string
}

func (c *cleanupTB) Helper()          {}
func (c *cleanupTB) Failed() bool     { return c.failed }
func (c *cleanupTB) Cleanup(f func()) { c.cleanups = append(c.cleanups, f) }
func (c *cleanupTB) Log(args ...any)  { c.logs = append(c.logs, fmt.Sprint(args...)) }
func (c *cleanupTB) Logf(format string, args ...any) {
	c.logs = append(c.logs, fmt.Sprintf(format, args...))
}
func (c *cleanupTB) runCleanups() {
	for i := len(c.cleanups) - 1; i >= 0; i-- {
		c.cleanups[i]()
	}
}

func TestLogger_KeepsTheLastRecordsAndDropsLateOnes(t *testing.T) {
	tb := &cleanupTB{TB: t, failed: true}
	logger := Logger(tb)
	for i := range maxLogRecords + 5 {
		logger.Info("record", "n", i)
	}
	tb.runCleanups()
	logger.Info("after the test", "n", -1)

	out := strings.Join(tb.logs, "\n")
	for _, want := range []string{"dropped 5 earlier records", fmt.Sprintf("n=%d", maxLogRecords+4), "n=5\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("flushed log does not contain %q", want)
		}
	}
	for _, unwanted := range []string{"n=4\n", "after the test"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("flushed log contains %q", unwanted)
		}
	}
}

func TestLogger_PassingTestPrintsNothing(t *testing.T) {
	tb := &cleanupTB{TB: t}
	Logger(tb).Error("rpc failed", "cause", "boom")
	tb.runCleanups()
	if len(tb.logs) != 0 {
		t.Fatalf("a passing test's flush wrote %q", tb.logs)
	}
}
