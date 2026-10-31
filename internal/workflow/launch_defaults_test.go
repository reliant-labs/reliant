// Copyright (c) 2025 Reliant Labs
package workflow_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
)

// Every launch form starts from a workflow's declared defaults and sends them
// as the run's inputs (the new-chat picker, Run now, an automation's form, a
// builder test). So a default that does not fit its own input's type is a run
// that fails before its first node for anyone who presses Run without editing
// it — the class of chat 34cf88e4, whose `repos` arrived as a string for a list
// of repositories. This suite renders each workflow's launch-form values from
// its schema and validates them against that schema, for every builtin and for
// every project workflow in the repos this one ships with.
//
// It also pins the boundary's one coercion against real schemas: a single item
// of a typed list input (the first item of its default) is read as a one-item
// list (validation.CoerceSingleToList) and then validates.

// launchDefaultsWorkflowDirs are the project workflow directories checked
// beside the builtins: this repo's own, and the sibling control-plane and forge
// checkouts when they sit next to it (as in the reliant-labs workspace).
// RELIANT_LAUNCH_DEFAULTS_DIRS (os.PathListSeparator-separated) adds more, for
// a worktree whose siblings live elsewhere.
func launchDefaultsWorkflowDirs() []string {
	root := filepath.Join("..", "..")
	dirs := []string{
		filepath.Join(root, ".reliant", "workflows"),
		filepath.Join(root, "..", "control-plane", ".reliant", "workflows"),
		filepath.Join(root, "..", "forge", ".reliant", "workflows"),
	}
	if extra := os.Getenv("RELIANT_LAUNCH_DEFAULTS_DIRS"); extra != "" {
		dirs = append(dirs, filepath.SplitList(extra)...)
	}
	return dirs
}

func TestEveryWorkflowsLaunchFormDefaultsValidate(t *testing.T) {
	for _, file := range builtinWorkflowFiles(t) {
		t.Run("builtin/"+file, func(t *testing.T) {
			assertLaunchDefaultsValidate(t, parseBuiltinWorkflow(t, file))
		})
	}

	for _, dir := range launchDefaultsWorkflowDirs() {
		files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
		sort.Strings(files)
		for _, path := range files {
			t.Run(path, func(t *testing.T) {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				wf, err := runtime.ParseWorkflowProtoBytes(data)
				require.NoError(t, err, "parse %s", path)
				assertLaunchDefaultsValidate(t, wf)
			})
		}
	}
}

// assertLaunchDefaultsValidate renders wf's launch-form values (each input's
// declared default, groups nested as a launch sends them) and validates them
// against wf's own schema.
func assertLaunchDefaultsValidate(t *testing.T, wf *reliantv1.Workflow) {
	t.Helper()
	form := launchFormDefaults(wf.GetInputs())
	assert.Empty(t, validation.ValidateParams(wf.GetInputs(), form),
		"%s: a launch form's defaults must validate against the workflow's own inputs", wf.GetName())

	for path, single := range singleItemsOfTypedLists(wf.GetInputs(), form) {
		sent := map[string]any{path: single}
		declared := wf.GetInputs()
		if group, name, nested := strings.Cut(path, "."); nested {
			sent = map[string]any{group: map[string]any{name: single}}
			declared = map[string]*reliantv1.Input{group: wf.GetInputs()[group]}
		}
		coerced := validation.CoerceSingleToList(sent, declared, nil)
		require.Len(t, coerced, 1, "%s: input %s given one valid item %v is read as a one-item list", wf.GetName(), path, single)
		assert.Empty(t, validation.ValidateParams(declared, sent), "%s: input %s", wf.GetName(), path)
	}
}

// launchFormDefaults is what a launch form holds before anyone edits it: the
// declared default of every input that has one, a group's nested under it.
func launchFormDefaults(inputs map[string]*reliantv1.Input) map[string]any {
	form := map[string]any{}
	for name, input := range inputs {
		if model.IsGroupInput(input) {
			if nested := launchFormDefaults(model.GetGroupInputs(input)); len(nested) > 0 {
				form[name] = nested
			}
			continue
		}
		if def := model.GetInputDefault(input); def != nil {
			form[name] = def
		}
	}
	return form
}

// singleItemsOfTypedLists maps each typed list input (a multi integration,
// enum or preset input, or tools) whose default holds an item to that first
// item, by input path ("<group>.<input>" inside a group).
func singleItemsOfTypedLists(inputs map[string]*reliantv1.Input, form map[string]any) map[string]any {
	out := map[string]any{}
	for name, input := range inputs {
		if model.IsGroupInput(input) {
			nestedForm, _ := form[name].(map[string]any)
			for path, item := range singleItemsOfTypedLists(model.GetGroupInputs(input), nestedForm) {
				out[name+"."+path] = item
			}
			continue
		}
		typedList := input.GetIntegrationInput().GetMulti() || input.GetEnumInput().GetMulti() ||
			input.GetPresetInput().GetMulti() || model.GetInputType(input) == "tools"
		items, _ := form[name].([]any)
		if typedList && len(items) > 0 {
			out[name] = items[0]
		}
	}
	return out
}
