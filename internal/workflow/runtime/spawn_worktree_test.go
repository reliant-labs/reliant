// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"fmt"
	"strings"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

func (s *SpawnTestSuite) TestParseSpawnToolInput_Worktree() {
	got, err := parseSpawnToolInput(`{"preset":"coder","prompt":"go","worktree":"  feature-x "}`)
	s.NoError(err)
	s.Equal("feature-x", got.worktree)

	got, err = parseSpawnToolInput(`{"preset":"coder","prompt":"go"}`)
	s.NoError(err)
	s.Empty(got.worktree, "omitting worktree keeps the chat's workspace")
}

// The requested worktree reaches the activity that creates the child thread,
// which is where it is validated and bound.
func (s *SpawnTestSuite) TestPrepareSpawnInline_PassesWorktreeToThreadCreation() {
	env := s.NewTestWorkflowEnvironment()
	s.registerActivityStubs(env)

	var gotWorktree interface{}
	env.RegisterActivityWithOptions(func(_ context.Context, input map[string]interface{}) (interface{}, error) {
		gotWorktree = input["worktree"]
		return nil, nil
	}, activity.RegisterOptions{Name: "CreateWorkflowWithThread"})

	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		prep := prepareSpawnInline(ctx, &spawnChildWorkflowConfig{
			childWorkflowID: "child-wf", childThread: "child-thread",
			promptStr: "do it", toolCallID: "tc-1", presetName: "coder", worktree: "feature-x",
		}, "/project", "chat-1", "parent-wf", "parent-thread",
			map[string]interface{}{}, func(string) *PauseController { return nil })
		if prep.earlyResult != nil {
			return fmt.Errorf("unexpected early result: %s", prep.earlyResult.Content)
		}
		return nil
	})
	s.NoError(env.GetWorkflowError())
	s.Equal("feature-x", gotWorktree)
}

func (s *SpawnTestSuite) TestPrepareSpawnInline_OmittedWorktreeSendsNone() {
	env := s.NewTestWorkflowEnvironment()
	s.registerActivityStubs(env)

	var input map[string]interface{}
	env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (interface{}, error) {
		input = in
		return nil, nil
	}, activity.RegisterOptions{Name: "CreateWorkflowWithThread"})

	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		prep := prepareSpawnInline(ctx, &spawnChildWorkflowConfig{
			childWorkflowID: "child-wf", childThread: "child-thread",
			promptStr: "do it", toolCallID: "tc-1", presetName: "coder",
		}, "/project", "chat-1", "parent-wf", "parent-thread",
			map[string]interface{}{}, func(string) *PauseController { return nil })
		if prep.earlyResult != nil {
			return fmt.Errorf("unexpected early result: %s", prep.earlyResult.Content)
		}
		return nil
	})
	s.NoError(env.GetWorkflowError())
	_, present := input["worktree"]
	s.False(present)
}

// A refused worktree comes back as the tool result the agent reads, as an
// error, and no child runs.
func (s *SpawnTestSuite) TestPrepareSpawnInline_RefusedWorktreeIsAToolError() {
	env := s.NewTestWorkflowEnvironment()
	s.registerActivityStubs(env)
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{"refusal": `Cannot spawn in worktree "nope": it does not exist in this project. Active worktrees of this project: "good".`}, nil
	}, activity.RegisterOptions{Name: "CreateWorkflowWithThread"})

	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		prep := prepareSpawnInline(ctx, &spawnChildWorkflowConfig{
			childWorkflowID: "child-wf", childThread: "child-thread",
			promptStr: "do it", toolCallID: "tc-1", presetName: "coder", worktree: "nope",
		}, "/project", "chat-1", "parent-wf", "parent-thread",
			map[string]interface{}{}, func(string) *PauseController { return nil })
		if prep.earlyResult == nil || !prep.earlyResult.IsError {
			return fmt.Errorf("expected an early error result")
		}
		if prep.earlyResult.ToolCallID != "tc-1" || !strings.Contains(prep.earlyResult.Content, `"good"`) {
			return fmt.Errorf("unexpected result: %+v", prep.earlyResult)
		}
		return nil
	})
	s.NoError(env.GetWorkflowError())
}

// A thread-bound child's working directory is its worktree, not the parent's,
// and a relaunch after continue-as-new restores it from the handoff.
func (s *SpawnTestSuite) TestPrepareSpawnInline_ChildProjectPathIsTheWorktree() {
	env := s.NewTestWorkflowEnvironment()
	s.registerActivityStubs(env)
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
		return map[string]interface{}{"worktree_path": "/wt/feature-x"}, nil
	}, activity.RegisterOptions{Name: "CreateWorkflowWithThread"})

	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		config := &spawnChildWorkflowConfig{
			childWorkflowID: "child-wf", childThread: "child-thread",
			promptStr: "do it", toolCallID: "tc-1", presetName: "coder", worktree: "feature-x",
		}
		prep := prepareSpawnInline(ctx, config, "/main/checkout", "chat-1", "parent-wf", "parent-thread",
			map[string]interface{}{}, func(string) *PauseController { return nil })
		if prep.earlyResult != nil {
			return fmt.Errorf("unexpected early result: %s", prep.earlyResult.Content)
		}
		if got := prep.childExecContext.ProjectPath; got != "/wt/feature-x" {
			return fmt.Errorf("child ProjectPath = %q, want the worktree", got)
		}
		handoff := spawnHandoffFor(config, prep, "parent-wf", "parent-thread")
		relaunch := prepareSpawnRelaunch(&spawnChildWorkflowConfig{
			toolCallID: "tc-1", childWorkflowID: "child-wf", childThread: "child-thread", isResumption: true,
		}, handoff, "chat-1", "/main/checkout", map[string]interface{}{}, func(string) *PauseController { return nil })
		if got := relaunch.childExecContext.ProjectPath; got != "/wt/feature-x" {
			return fmt.Errorf("relaunched child ProjectPath = %q, want the worktree", got)
		}
		return nil
	})
	s.NoError(env.GetWorkflowError())
}
