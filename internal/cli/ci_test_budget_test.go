package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runTestBudget(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := newCITestBudgetCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// A real -short run (internal/testbudget's fixture module) with one unmarked
// 2.5s test and a 2.4s table: the command fails, names both with file:line.
func TestCITestBudget_FlagsUnmarkedSlowTestsWithFileAndLine(t *testing.T) {
	stream, err := os.ReadFile("../testbudget/testdata/short.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir("../testbudget/testdata/budgetfixture")
	out, err := runTestBudget(t, string(stream))
	if err == nil {
		t.Fatalf("two over-budget tests exited 0:\n%s", out)
	}
	for _, want := range []string{"3 test(s) ran, 2 over budget", "TestSlowUnmarked", "budget_test.go:14", "TestTable", "budget_test.go:28"} {
		if !strings.Contains(out, want) {
			t.Errorf("report does not contain %q:\n%s", want, out)
		}
	}
	if !strings.Contains(err.Error(), "testkit.Slow") {
		t.Errorf("the error does not say how to mark a test slow: %v", err)
	}
}

func TestCITestBudget_WithinBudgetPasses(t *testing.T) {
	stream, err := os.ReadFile(filepath.Join("..", "testbudget", "testdata", "short.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := runTestBudget(t, string(stream), "--budget", "3s")
	if err != nil {
		t.Fatalf("a run within a 3s budget failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "0 over budget") {
		t.Errorf("the pass does not state what it checked:\n%s", out)
	}
}

func TestCITestBudget_WarnOnlyReportsWithoutFailing(t *testing.T) {
	out, err := runTestBudget(t, `{"Action":"pass","Package":"example.com/m","Test":"TestSlow","Elapsed":9}`, "--warn-only")
	if err != nil || !strings.Contains(out, "TestSlow") || !strings.Contains(out, "warn-only") {
		t.Fatalf("--warn-only: err=%v\n%s", err, out)
	}
}

func TestCITestBudget_FailuresAndEmptyInputAreNotAPass(t *testing.T) {
	if _, err := runTestBudget(t, `{"Action":"fail","Package":"example.com/m","Elapsed":1}`); err == nil || !strings.Contains(err.Error(), "failures") {
		t.Errorf("a failed package passed the budget check: %v", err)
	}
	if _, err := runTestBudget(t, "PASS\n"); err == nil || !strings.Contains(err.Error(), "UNDETERMINED") {
		t.Errorf("input with no events passed: %v", err)
	}
}
