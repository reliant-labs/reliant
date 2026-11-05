package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli/factory"
)

// A step that fails through warnOrFail lets the run go on with the steps that
// do not need it. It used to let the run exit 0 as well, after printing
// "1 step(s) FAILED": a false green to every agent, script and CI gate that
// reads exit 0 as success. Pinned: the run still continues and keeps its
// writes, and it fails with a *factory.StepsFailedError naming the steps.

// softFailingStep fails the way every warnOrFail site does.
func softFailingStep(name string, times int) GenStep {
	return GenStep{
		Name: name,
		Gate: always,
		Tag:  "codegen",
		Run: func(ctx *pipelineContext) error {
			for range times {
				if err := ctx.warnOrFail(name, errors.New("simulated failure")); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// laterWriter is an independent step after the failing one; it writes one
// forge file so the test can see whether it ran and whether its write stood.
func laterWriter() GenStep {
	return GenStep{
		Name: "later writer",
		Gate: always,
		Tag:  "codegen",
		Run: func(ctx *pipelineContext) error {
			_, err := checksums.WriteGeneratedFile(ctx.AbsPath, "internal/later_gen.go", []byte("package internal\n"), &checksums.FileChecksums{}, false)
			return err
		},
	}
}

func TestGenerate_AFailedStepFailsTheRunItDidNotStop(t *testing.T) {
	dir := t.TempDir()
	writeTreeFileT(t, filepath.Join(dir, "internal", "x.go"), "package internal\n")

	withPipelineSteps(t, []GenStep{softFailingStep("widget generation", 1), laterWriter()})
	out, runErr := runPipelineCapturingOutput(t, dir)

	var stepsFailed *factory.StepsFailedError
	if !errors.As(runErr, &stepsFailed) {
		t.Fatalf("a run with a failed step must fail with *factory.StepsFailedError, got %v\n%s", runErr, out)
	}
	if want := []string{"widget generation"}; !reflect.DeepEqual(stepsFailed.Steps, want) {
		t.Errorf("Steps = %v, want %v", stepsFailed.Steps, want)
	}
	if !strings.Contains(runErr.Error(), "widget generation") {
		t.Errorf("the error must name the failed step: %v", runErr)
	}
	// The run went on past the failure, and what it wrote stands.
	if _, err := os.Stat(filepath.Join(dir, "internal", "later_gen.go")); err != nil {
		t.Errorf("the step after the failed one must run and keep its write: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 step(s) FAILED") || !strings.Contains(out, "exits non-zero") {
		t.Errorf("the closing summary must name the failure and say the run fails:\n%s", out)
	}
}

// --strict keeps its meaning: stop at the failed step (and roll back).
func TestGenerate_StrictStopsAtTheFailedStep(t *testing.T) {
	dir := t.TempDir()
	writeTreeFileT(t, filepath.Join(dir, "internal", "x.go"), "package internal\n")

	withPipelineSteps(t, []GenStep{softFailingStep("widget generation", 1), laterWriter()})
	stderr, restore := captureStderr(t)
	var runErr error
	out := captureStdout(t, func() {
		runErr = runGeneratePipelineFlags(dir, pipelineFlags{Strict: true})
	})
	restore()
	out += stderr.String()

	if runErr == nil || !strings.Contains(runErr.Error(), "(--strict)") {
		t.Fatalf("--strict must fail the run at the step, got %v\n%s", runErr, out)
	}
	var stepsFailed *factory.StepsFailedError
	if errors.As(runErr, &stepsFailed) {
		t.Errorf("--strict aborts the run; it must not report a run that reached the end: %v", runErr)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal", "later_gen.go")); !os.IsNotExist(err) {
		t.Errorf("--strict must stop before the next step, stat err = %v", err)
	}
}

// A step that fails several times (one stale scaffold test per file) is one
// failed step, and a clean run is no error at all.
func TestGenerate_StepsFailedCountsSteps(t *testing.T) {
	dir := t.TempDir()
	writeTreeFileT(t, filepath.Join(dir, "internal", "x.go"), "package internal\n")

	withPipelineSteps(t, []GenStep{softFailingStep("stale scaffold test check", 3), laterWriter()})
	out, runErr := runPipelineCapturingOutput(t, dir)
	var stepsFailed *factory.StepsFailedError
	if !errors.As(runErr, &stepsFailed) || len(stepsFailed.Steps) != 1 {
		t.Fatalf("three failures of one step are one failed step, got %v\n%s", runErr, out)
	}
	if !strings.Contains(out, "1 step(s) FAILED") {
		t.Errorf("summary must count the step once:\n%s", out)
	}

	clean := t.TempDir()
	writeTreeFileT(t, filepath.Join(clean, "internal", "x.go"), "package internal\n")
	withPipelineSteps(t, []GenStep{laterWriter()})
	if out, err := runPipelineCapturingOutput(t, clean); err != nil {
		t.Errorf("a clean run must not fail: %v\n%s", err, out)
	}
}

// --watch keeps watching after a run that reached the end with failed steps;
// every other failure still ends the command.
func TestGenerateRunEndsCommand(t *testing.T) {
	stepsFailed := &factory.StepsFailedError{Steps: []string{"ORM generation"}}
	aborted := errors.New(`step "go build (validate generated code)": validation failed`)
	for _, tc := range []struct {
		name  string
		err   error
		watch bool
		want  bool
	}{
		{"clean", nil, false, false},
		{"clean, watching", nil, true, false},
		{"steps failed", stepsFailed, false, true},
		{"steps failed, watching", stepsFailed, true, false},
		{"aborted", aborted, false, true},
		{"aborted, watching", aborted, true, true},
	} {
		if got := generateRunEndsCommand(tc.err, tc.watch); got != tc.want {
			t.Errorf("%s: generateRunEndsCommand = %v, want %v", tc.name, got, tc.want)
		}
	}
}
