// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools/names"
)

var runToolNames = []string{ToolStartRun, ToolListRuns, ToolGetRun, ToolControlRun, ToolSendToRun}

// Every run-management tool must be in the registry, reachable through the
// factory under its own name, and known to the name validator workflow YAML is
// checked against — a name in only some of those is effectively unregistered.
func TestRunTools_AreRegisteredAndConstructible(t *testing.T) {
	t.Parallel()

	registered := map[string]ToolDefinition{}
	for _, def := range GetToolRegistry() {
		registered[def.Name] = def
	}
	validatorNames := map[string]bool{}
	for _, name := range names.AllToolNames {
		validatorNames[name] = true
	}

	factory := NewToolsFactory(&ToolsOptions{})
	for _, name := range runToolNames {
		def, ok := registered[name]
		require.True(t, ok, "%s missing from GetToolRegistry()", name)
		assert.Equal(t, ToolRunsOnServer, def.RunsOn, "%s reaches the database and the launcher, so it executes on the server", name)
		assert.True(t, validatorNames[name], "%s missing from names.AllToolNames", name)

		tool := factory.GetToolByName(name, nil)
		require.NotNil(t, tool, "factory.GetToolByName(%q) returned nil", name)
		assert.Equal(t, name, tool.Name())
		assert.NotEmpty(t, tool.Description())
	}
}

// The tools are NOT in the coding agent's default bundle: an agent that was
// never given the ability to start runs should not be offered it. They carry
// the descriptive `runs` tag so a workflow can grant them as a group.
func TestRunTools_CarryTheRunsTagAndStayOutOfCodingDefault(t *testing.T) {
	t.Parallel()
	for _, def := range GetToolRegistry() {
		isRunTool := false
		for _, name := range runToolNames {
			if def.Name == name {
				isRunTool = true
			}
		}
		if !isRunTool {
			for _, tag := range def.Tags {
				assert.NotEqual(t, TagRuns, tag, "%s carries tag:runs but is not a run-management tool", def.Name)
			}
			continue
		}
		assert.Contains(t, def.Tags, TagRuns, "%s must carry tag:runs", def.Name)
		assert.NotContains(t, def.Tags, TagCodingDefault, "%s must not be in tag:coding:default", def.Name)
		assert.NotContains(t, def.Tags, TagCodingPlan, "%s must not be in tag:coding:plan", def.Name)
	}
}

// The reading tools are readonly-tagged (plan mode may use them); the ones that
// start, stop or steer work are not.
func TestRunTools_ReadonlyTagOnlyOnTheReaders(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		ToolListRuns: true, ToolGetRun: true,
		ToolStartRun: false, ToolControlRun: false, ToolSendToRun: false,
	}
	for _, def := range GetToolRegistry() {
		readonly, isRunTool := want[def.Name]
		if !isRunTool {
			continue
		}
		hasTag := false
		for _, tag := range def.Tags {
			if tag == TagReadOnly {
				hasTag = true
			}
		}
		assert.Equal(t, readonly, hasTag, "%s readonly tag", def.Name)
	}
}

// `runs` must have a description (TestTagDescriptionsAreComplete covers the
// general rule; this pins that the new tag is the one being described) and be
// mirrored in the workflow validator's tag list.
func TestRunsTagIsDescribedAndMirrored(t *testing.T) {
	t.Parallel()
	assert.NotEmpty(t, TagDescriptions[TagRuns])
	assert.Contains(t, names.AllToolTags, string(TagRuns))
}

// Run-id parameters name one distinct run per call; binding one would aim every
// call at the same run.
func TestRunTools_RunIDIsUnbindable(t *testing.T) {
	t.Parallel()
	for _, name := range []string{ToolGetRun, ToolControlRun, ToolSendToRun} {
		_, ok := UnbindableParams(name)["run_id"]
		assert.True(t, ok, "%s.run_id must be unbindable", name)
	}
}
