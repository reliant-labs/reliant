// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

// captureStdout returns what fn wrote to os.Stdout. The workflow validate and
// scenario commands print there directly rather than to cmd.OutOrStdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	prev := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		data, _ := io.ReadAll(r)
		done <- data
	}()
	defer func() { os.Stdout = prev }()
	fn()
	require.NoError(t, w.Close())
	return string(<-done)
}

// embeddedBuiltinNames is every builtin workflow file name the binary carries,
// without its extension.
func embeddedBuiltinNames(t *testing.T) []string {
	t.Helper()
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
	}
	require.NotEmpty(t, names)
	return names
}

// Built-in workflows are embedded in the binary. Listing them used to look for
// internal/workflow/builtin under the current directory, so outside a reliant
// source checkout `workflow list --builtins-only` found none.
func TestWorkflowListFindsEmbeddedBuiltinsOutsideTheSourceCheckout(t *testing.T) {
	t.Chdir(t.TempDir())

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"workflow", "list", "--builtins-only", "--json"})
	require.NoError(t, root.Execute())

	var listed []workflowListInfo
	require.NoError(t, json.Unmarshal(out.Bytes(), &listed), out.String())
	require.Len(t, listed, len(embeddedBuiltinNames(t)))
	for _, wf := range listed {
		require.Equal(t, "builtin", wf.Source)
		require.NotEmpty(t, wf.Name)
		require.Positive(t, wf.Nodes, "%s parsed", wf.File)
	}
}

// The same defect in `workflow validate --include-builtins`.
func TestWorkflowValidateIncludesEmbeddedBuiltinsOutsideTheSourceCheckout(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".reliant", "workflows"), 0o755))

	out := captureStdout(t, func() {
		_ = runWorkflowValidate(nil, nil, ".reliant/workflows", false, false, true, true, nil, "")
	})

	var results []validateResult
	require.NoError(t, json.Unmarshal([]byte(out), &results), out)
	files := map[string]bool{}
	for _, r := range results {
		files[r.File] = true
	}
	for _, name := range embeddedBuiltinNames(t) {
		require.True(t, files[name+".yaml"], "builtin %s was not validated; got %v", name, files)
	}
}

// --filter narrows what runs, and the count announced up front must be the
// count that runs.
func TestWorkflowScenarioRunCountsOnlyFilteredScenarios(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "denied-loop.yaml"), []byte(deniedLoopWorkflow), 0o644))
	scenarios := filepath.Join(dir, "denied-loop", "scenarios")
	require.NoError(t, os.MkdirAll(scenarios, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(scenarios, "denied_once.yaml"), []byte(deniedOnceScenario), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(scenarios, "other.yaml"),
		[]byte(strings.Replace(deniedOnceScenario, "name: denied_once", "name: other", 1)), 0o644))

	out := captureStdout(t, func() {
		_ = runWorkflowScenarios(nil, []string{dir}, "", false, false, false, false, "other")
	})

	require.Contains(t, out, "Running 1 scenario(s) across 1 workflow(s)")
}
