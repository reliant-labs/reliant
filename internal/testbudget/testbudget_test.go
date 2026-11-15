package testbudget

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/pkg/testkit"
)

const fixtureModule = "testdata/budgetfixture"

// assertFixtureVerdict is the verdict on budgetfixture's -short run, wherever
// the stream came from.
func assertFixtureVerdict(t *testing.T, stream []byte) {
	t.Helper()
	rep, err := Check(bytes.NewReader(stream), DefaultBudget)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	Locate(&rep, fixtureModule)

	// TestFast, TestSlowUnmarked, TestTable ran; TestMarkedSlow skipped.
	if rep.Ran != 3 {
		t.Errorf("Ran = %d, want 3 (the marked test skips under -short and is not counted)", rep.Ran)
	}
	if len(rep.FailedPackages) != 0 {
		t.Errorf("FailedPackages = %v, want none", rep.FailedPackages)
	}
	var got []string
	for _, o := range rep.Overruns {
		got = append(got, o.Test)
		if o.File != "budget_test.go" || o.Line == 0 {
			t.Errorf("%s is not located: file=%q line=%d", o.Test, o.File, o.Line)
		}
		if o.Marked {
			t.Errorf("%s reported as marked; it carries no testing.Short/testkit.Slow call", o.Test)
		}
		if o.Elapsed <= DefaultBudget {
			t.Errorf("%s reported at %v, which is within the budget", o.Test, o.Elapsed)
		}
	}
	// Slowest first; the table is judged as a whole, its subtests never.
	if strings.Join(got, ",") != "TestSlowUnmarked,TestTable" {
		t.Errorf("overruns = %v, want [TestSlowUnmarked TestTable]", got)
	}
}

// The recorded stream is real `go test -short -json` output of the fixture
// module (regenerate with the command in TestCheckLiveRun).
func TestCheckRecordedShortRun(t *testing.T) {
	t.Parallel()
	stream, err := os.ReadFile("testdata/short.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	assertFixtureVerdict(t, stream)
}

// The same verdict on a stream produced now, by the go toolchain this test
// runs under, so a change in `go test -json`'s shape cannot leave the
// recorded file vouching for a parser that no longer reads real output.
func TestCheckLiveRun(t *testing.T) {
	testkit.Slow(t, "runs `go test -short -json` on a fixture module whose tests sleep ~5s",
		testkit.Inputs("internal/testbudget/testdata/**"))
	t.Parallel()
	cmd := exec.Command("go", "test", "-short", "-count=1", "-json", "./...")
	cmd.Dir = fixtureModule
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go test on the fixture module: %v\n%s", err, out)
	}
	assertFixtureVerdict(t, out)
}

func TestCheckBudgetIsConfigurable(t *testing.T) {
	t.Parallel()
	stream, err := os.ReadFile("testdata/short.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Check(bytes.NewReader(stream), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Overruns) != 0 {
		t.Errorf("a 3s budget still reported %v", rep.Overruns)
	}
}

func TestCheckReportsFailuresAndMarkedOverruns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "package m\n\nimport \"testing\"\n\nfunc TestBypass(t *testing.T) {\n\tif !testing.Short() {\n\t\treturn\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "m_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	stream := strings.Join([]string{
		`{"Action":"pass","Package":"example.com/m","Test":"TestBypass","Elapsed":4.5}`,
		`{"Action":"fail","Package":"example.com/m","Elapsed":4.6}`,
		`{"ImportPath":"example.com/m/broken","Action":"build-fail"}`,
		`not json: build output`,
	}, "\n")
	rep, err := Check(strings.NewReader(stream), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	Locate(&rep, dir)
	if len(rep.Overruns) != 1 || !rep.Overruns[0].Marked || rep.Overruns[0].Line != 5 {
		t.Fatalf("overruns = %+v, want TestBypass at m_test.go:5, marked", rep.Overruns)
	}
	if strings.Join(rep.FailedPackages, ",") != "example.com/m,example.com/m/broken" {
		t.Errorf("FailedPackages = %v", rep.FailedPackages)
	}
}

func TestCheckEmptyInputIsNotAPass(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "ok  \texample.com/m\t0.1s\n"} {
		if _, err := Check(strings.NewReader(in), DefaultBudget); !errors.Is(err, ErrNoInput) {
			t.Errorf("Check(%q) error = %v, want ErrNoInput", in, err)
		}
	}
}

// A parent whose subtests call t.Parallel reports Elapsed 0: the testing
// package takes it when the parent's function returns, before the subtests
// run. Its wall time — run event to its own pass, after the last subtest's —
// is what it costs the blocking tier, and what is judged. The stream is a
// real `go test -json` run's shape (reliant #2459's TestMergeGatePgtestPhase:
// "--- PASS (0.00s)" over seven ~0.5s parallel cases), stretched past 2s.
func TestCheckJudgesParallelSubtestsByWallTime(t *testing.T) {
	t.Parallel()
	stream := strings.Join([]string{
		`{"Time":"2026-11-10T02:52:01.309Z","Action":"run","Package":"example.com/p","Test":"TestPar"}`,
		`{"Time":"2026-11-10T02:52:01.311Z","Action":"run","Package":"example.com/p","Test":"TestPar/a"}`,
		`{"Time":"2026-11-10T02:52:01.312Z","Action":"pause","Package":"example.com/p","Test":"TestPar/a"}`,
		`{"Time":"2026-11-10T02:52:01.312Z","Action":"run","Package":"example.com/p","Test":"TestPar/b"}`,
		`{"Time":"2026-11-10T02:52:01.312Z","Action":"pause","Package":"example.com/p","Test":"TestPar/b"}`,
		`{"Time":"2026-11-10T02:52:01.312Z","Action":"cont","Package":"example.com/p","Test":"TestPar/a"}`,
		`{"Time":"2026-11-10T02:52:01.312Z","Action":"cont","Package":"example.com/p","Test":"TestPar/b"}`,
		`{"Time":"2026-11-10T02:52:04.513Z","Action":"pass","Package":"example.com/p","Test":"TestPar/b","Elapsed":3.2}`,
		`{"Time":"2026-11-10T02:52:04.513Z","Action":"pass","Package":"example.com/p","Test":"TestPar/a","Elapsed":3.2}`,
		`{"Time":"2026-11-10T02:52:04.514Z","Action":"pass","Package":"example.com/p","Test":"TestPar","Elapsed":0}`,
		// A top-level t.Parallel test: paused 5s behind the others, ran 0.1s.
		// Its wait is not its cost: it is judged from its cont.
		`{"Time":"2026-11-10T02:52:00.000Z","Action":"run","Package":"example.com/p","Test":"TestWaits"}`,
		`{"Time":"2026-11-10T02:52:00.001Z","Action":"pause","Package":"example.com/p","Test":"TestWaits"}`,
		`{"Time":"2026-11-10T02:52:05.000Z","Action":"cont","Package":"example.com/p","Test":"TestWaits"}`,
		`{"Time":"2026-11-10T02:52:05.100Z","Action":"pass","Package":"example.com/p","Test":"TestWaits","Elapsed":0.1}`,
		`{"Time":"2026-11-10T02:52:05.200Z","Action":"pass","Package":"example.com/p","Elapsed":5.2}`,
	}, "\n")
	rep, err := Check(strings.NewReader(stream), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Ran != 2 {
		t.Errorf("Ran = %d, want 2 top-level tests", rep.Ran)
	}
	if len(rep.Overruns) != 1 || rep.Overruns[0].Test != "TestPar" {
		t.Fatalf("overruns = %+v, want TestPar alone: its parallel subtests took 3.2s of wall time", rep.Overruns)
	}
	if got := rep.Overruns[0].Elapsed; got < 3200*time.Millisecond || got > 3300*time.Millisecond {
		t.Errorf("TestPar judged at %v, want its ~3.2s wall time", got)
	}
}
