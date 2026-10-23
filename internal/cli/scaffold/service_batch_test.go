package scaffold

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
)

// TestRunServices_RunsPipelineOnceForTheBatch is the wall-clock claim. The
// generate pipeline is the dominant cost of `forge scaffold service`, and it
// derives everything it emits from the tree — so scaffolding four services
// means writing four sets of sources and projecting them ONCE, not paying for
// four full projections of which the first three are immediately superseded.
func TestRunServices_RunsPipelineOnceForTheBatch(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)

	runs := 0
	f := testFactory()
	f.Gen.RunPipeline = func(string) error { runs++; return nil }

	names := []string{"customers", "sales", "jobs", "billing"}
	if err := runServices(f, names, false, false); err != nil {
		t.Fatalf("runServices: %v", err)
	}
	if runs != 1 {
		t.Errorf("pipeline ran %d times for %d services, want exactly 1", runs, len(names))
	}
	// Every service must actually be on disk — batching must not skip work.
	for _, name := range names {
		for _, want := range []string{
			filepath.Join(dir, "internal", "handlers", name),
			filepath.Join(dir, "proto", "services", name),
		} {
			if _, err := os.Stat(want); err != nil {
				t.Errorf("service %q missing %s: %v", name, want, err)
			}
		}
	}
}

// TestRunServices_SingleNameMatchesRunService pins that the batch path is the
// ONLY path — a one-name call must behave exactly like the old single-service
// verb, pipeline included.
func TestRunServices_SingleNameMatchesRunService(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)

	runs := 0
	f := testFactory()
	f.Gen.RunPipeline = func(string) error { runs++; return nil }

	if err := runServices(f, []string{"orders"}, false, false); err != nil {
		t.Fatalf("runServices: %v", err)
	}
	if runs != 1 {
		t.Errorf("pipeline ran %d times for one service, want 1", runs)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "handlers", "orders")); err != nil {
		t.Errorf("single-name batch did not scaffold the service: %v", err)
	}
}

// TestRunServices_RejectsDuplicateNames catches the typo BEFORE any file is
// written. Two identical names in one call would scaffold the same dirs twice
// and then collide in codegen; the second one has to be rejected at the point
// the batch is read, which is the only point where it is still cheap.
func TestRunServices_RejectsDuplicateNames(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)

	f := testFactory()
	err := runServices(f, []string{"orders", "billing", "orders"}, false, false)
	if err == nil {
		t.Fatal("expected a duplicate-name rejection")
	}
	// Nothing may have been written — the check runs before any scaffold.
	if _, statErr := os.Stat(filepath.Join(dir, "internal", "handlers", "orders")); !os.IsNotExist(statErr) {
		t.Errorf("a rejected batch still wrote files (stat err=%v)", statErr)
	}
}

// TestRunServices_RollsBackEveryFreshServiceOnPipelineFailure extends the
// existing single-service revert guarantee to the batch: one pipeline failure
// must leave the tree in its pre-batch state, not with three of four services
// stranded.
func TestRunServices_RollsBackEveryFreshServiceOnPipelineFailure(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)

	f := testFactory()
	f.Gen.RunPipeline = func(string) error { return fmt.Errorf("simulated validation failure") }

	names := []string{"customers", "sales", "jobs"}
	if err := runServices(f, names, false, false); err == nil {
		t.Fatal("expected runServices to surface the pipeline failure")
	}
	for _, name := range names {
		for _, gone := range []string{
			filepath.Join(dir, "internal", "handlers", name),
			filepath.Join(dir, "proto", "services", name),
		} {
			if _, err := os.Stat(gone); !os.IsNotExist(err) {
				t.Errorf("batch rollback did not remove %s (stat err=%v)", gone, err)
			}
		}
	}
}

// TestRunServices_KeepsTheServicesWhenGenerateStepsFailed is the other kind of
// pipeline failure: the run reached the end with failed steps, and its writes
// — the wiring generated from these services' sources — stand. Rolling the
// sources back would leave that wiring pointing at deleted packages, so every
// service is kept and finished, and the scaffold still fails, naming the steps.
func TestRunServices_KeepsTheServicesWhenGenerateStepsFailed(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)

	f := testFactory()
	f.Gen.RunPipeline = func(string) error { return &factory.StepsFailedError{Steps: []string{"ORM generation"}} }

	names := []string{"customers", "sales"}
	err := runServices(f, names, false, false)
	var stepsFailed *factory.StepsFailedError
	if !errors.As(err, &stepsFailed) {
		t.Fatalf("runServices must fail with the generate run's failed steps, got %v", err)
	}
	if !strings.Contains(err.Error(), "ORM generation") {
		t.Errorf("the error must name the failed step: %v", err)
	}
	for _, name := range names {
		for _, kept := range []string{
			filepath.Join(dir, "internal", "handlers", name),
			filepath.Join(dir, "proto", "services", name),
		} {
			if _, statErr := os.Stat(kept); statErr != nil {
				t.Errorf("a run whose writes stand must not roll back %s: %v", kept, statErr)
			}
		}
	}
}

// TestRunServices_RequiresAtLeastOneName keeps the cobra arity contract honest
// at the function boundary too.
func TestRunServices_RequiresAtLeastOneName(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)
	if err := runServices(testFactory(), nil, false, false); err == nil {
		t.Fatal("expected an error for an empty batch")
	}
}

// TestServiceCmd_AcceptsMultipleNames is the CLI surface: the whole point is
// that `forge scaffold service customers sales jobs billing` is ONE call.
func TestServiceCmd_AcceptsMultipleNames(t *testing.T) {
	dir := withTempProject(t, "name: testproj\nmodule_path: example.com/testproj\n")
	markServiceProject(t, dir)

	runs := 0
	f := testFactory()
	f.Gen.RunPipeline = func(string) error { runs++; return nil }

	cmd := newServiceCmd(f)
	cmd.SetOut(os.Stdout)
	cmd.SetArgs([]string{"customers", "sales", "jobs", "billing"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("scaffold service (multi): %v", err)
	}
	if runs != 1 {
		t.Errorf("pipeline ran %d times, want 1", runs)
	}
	for _, name := range []string{"customers", "sales", "jobs", "billing"} {
		if _, err := os.Stat(filepath.Join(dir, "internal", "handlers", name)); err != nil {
			t.Errorf("service %q was not scaffolded: %v", name, err)
		}
	}
}
