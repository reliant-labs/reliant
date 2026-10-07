// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// No shipped workflow may run a sync-with-delete, or a recursive remove whose
// target is interpolated. parallel-compete's
// `rsync -av --delete <winner>/ "{{workflow.path}}/"` (2026-10-06) is the
// reason: one unset variable turned it into `rsync --delete <winner>/ /`. A
// workflow that needs to replace files applies a patch (git apply cannot write
// outside its repository) or names its targets literally.
var (
	syncWithDelete       = regexp.MustCompile(`\brsync\b[^\n]*--del`)
	interpolatedRecursRm = regexp.MustCompile(`\brm\s+(-[a-zA-Z]*[rR][a-zA-Z]*\s+|--recursive\s+)[^\n;&|]*\{\{`)
)

func TestBuiltinRunCommands_NoDeletingCommandOnAnInterpolatedPath(t *testing.T) {
	t.Parallel()
	files, err := fs.Glob(builtin.BuiltinWorkflowsFS, "*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no builtin workflows found: %v", err)
	}
	checked := 0
	for _, file := range files {
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		wf, err := wfyaml.ParseWorkflow(data)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		walkRunCommands(wf, file, func(where, command string) {
			checked++
			for _, line := range strings.Split(command, "\n") {
				if syncWithDelete.MatchString(line) {
					t.Errorf("%s: run command uses rsync --delete:\n  %s", where, strings.TrimSpace(line))
				}
				if interpolatedRecursRm.MatchString(line) {
					t.Errorf("%s: run command recursively removes an interpolated path:\n  %s", where, strings.TrimSpace(line))
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("found no run commands to check; the walk is broken")
	}
}

func walkRunCommands(wf *reliantv1.Workflow, where string, visit func(where, command string)) {
	for _, node := range wf.GetNodes() {
		at := where + ":" + node.GetId()
		if run := node.GetRun(); run != nil {
			visit(at, model.CelStringRaw(run.GetCommand()))
		}
		if inline := model.NodeInlineWorkflow(node); inline != nil {
			walkRunCommands(inline, at, visit)
		}
	}
}
