// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// =============================================================================
// YAML NAVIGATION HELPERS
//
// These read the workflow as data rather than as text so an assertion names the
// field it is about ("the reviewer's inject", "build_mvp's review_tools") instead
// of a substring that happens to appear near it.
// =============================================================================

func loadWorkflowYAML(t *testing.T, file string) map[string]interface{} {
	t.Helper()
	data, err := builtin.BuiltinWorkflowsFS.ReadFile(file)
	require.NoError(t, err, "read %s", file)
	var doc map[string]interface{}
	require.NoError(t, yaml.Unmarshal(data, &doc), "parse %s", file)
	return doc
}

// nodeByID finds a node in a `nodes:` sequence by its id.
func nodeByID(t *testing.T, container map[string]interface{}, id string) map[string]interface{} {
	t.Helper()
	nodes, ok := container["nodes"].([]interface{})
	require.True(t, ok, "expected a nodes: sequence")
	for _, raw := range nodes {
		node, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if node["id"] == id {
			return node
		}
	}
	t.Fatalf("no node with id %q", id)
	return nil
}

func mapAt(t *testing.T, m map[string]interface{}, key string) map[string]interface{} {
	t.Helper()
	sub, ok := m[key].(map[string]interface{})
	require.True(t, ok, "expected %q to be a mapping, got %T", key, m[key])
	return sub
}

// injectContent returns the `thread.inject.content` of a node inside a loop's
// inline graph — the literal prompt text the workflow ships.
func injectContent(t *testing.T, file, loopNodeID, innerNodeID string) string {
	t.Helper()
	doc := loadWorkflowYAML(t, file)
	loop := nodeByID(t, doc, loopNodeID)
	inline := mapAt(t, loop, "inline")
	node := nodeByID(t, inline, innerNodeID)
	inject := mapAt(t, mapAt(t, node, "thread"), "inject")
	content, ok := inject["content"].(string)
	require.True(t, ok, "%s.%s inject content must be a string", loopNodeID, innerNodeID)
	return content
}

func stringSlice(t *testing.T, v interface{}) []string {
	t.Helper()
	raw, ok := v.([]interface{})
	require.True(t, ok, "expected a sequence, got %T", v)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		require.True(t, ok, "expected a string element, got %T", item)
		out = append(out, s)
	}
	return out
}

// =============================================================================

// toolsNamedIn returns the tool names an instruction text ORDERS the agent to use.
//
// The rule is deliberately narrow and mechanical: a REGISTERED tool name appearing
// in backticks is a tool the prose names. Scanning per registry name rather than
// pairing off backticks is not a style choice — the first version of this matched
// backtick pairs left to right with `([^`\n]+)` and found NOTHING, because the
// reviewer's prompt contains a fenced code block. The three fence backticks shift
// every pair after them by one, so `bash_output` was read as the text BETWEEN a
// closing and an opening tick. A derived check that silently matches nothing is
// the same defect this file exists to catch, so TestToolNameExtractionIsNotVacuous
// below holds it to a known-present name.
//
// Scanning by name also gives the narrowness for free: `forge build` never matches
// (the token is "forge build", not "build"), and neither do `live_url` or
// `pages_inspected`, which are response-schema fields rather than tools.
func toolsNamedIn(text string) []string {
	var named []string
	for _, def := range tools.GetToolRegistry() {
		if strings.Contains(text, "`"+def.Name+"`") {
			named = append(named, def.Name)
		}
	}
	sort.Strings(named)
	return named
}

// TestReviewerHoldsEveryToolItsInstructionsName derives the assertion from the
// prompt instead of pinning a list, because the LIST was never the defect — the
// DRIFT between the list and the prose was.
//
// Measured, run against a complete and working 23-RPC app: the reviewer's step 1
// ordered it to "READ that line … out of `bash_output` / `bash_list` and use THAT
// port everywhere". `review_tools` was [view, tag:shell, component_library,
// mcp__chrome-devtools__*], and `tag:shell` carried the shell tool ALONE —
// bash_list and bash_output were tagged execution/readonly/plan/default and
// nothing else. Step 1 gates `live_url`, which the response schema REQUIRES
// alongside a non-empty `pages_inspected`, so with no legal way to obtain the port
// the only in-schema move left was an evidence-free `stuck`. That is what it
// filed, after 13.5 seconds and a single LLM call:
//
//	{"grade":"fail","strategy":"stuck",
//	 "feedback":"Unable to perform the required review inspection in this turn…
//	   This is a tooling/execution-order blocker for this review attempt, not
//	   evidence that the implementation is incorrect.",
//	 "inspection_evidence":{"live_url":"not obtained", …}}
//
// A required field with no reachable way to fill it is not a strict schema, it is
// a dead end. Every prompt that names a tool must be able to reach it.
func TestReviewerHoldsEveryToolItsInstructionsName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		tools []string
	}{
		{
			name:  "get-it-right default reviewer",
			text:  injectContent(t, "get-it-right.yaml", "attempt", "review"),
			tools: defaultReviewTools(t),
		},
		{
			name:  "forge-one-shot build_mvp reviewer",
			text:  forgeOneShotReviewInstructions(t),
			tools: forgeOneShotReviewTools(t),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			granted := make(map[string]bool)
			for _, name := range tools.ExpandToolFilter(tc.tools, nil) {
				granted[name] = true
			}
			named := toolsNamedIn(tc.text)
			for _, tool := range named {
				require.True(t, granted[tool],
					"the review instructions order the agent to use %q, but review_tools %v "+
						"resolves to %v — a prompt that names a tool the grant does not carry "+
						"leaves the reviewer no legal way to satisfy its own response schema, and "+
						"the only in-schema move left is an evidence-free `stuck`",
					tool, tc.tools, sortedKeys(granted))
			}
			t.Logf("%s names %v; grant resolves to %d tools", tc.name, named, len(granted))
		})
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestToolNameExtractionIsNotVacuous keeps the check above honest.
//
// A derived assertion that matches nothing passes for the wrong reason, and this
// one already did once: the first extractor paired backticks left to right, the
// reviewer's prompt contains a fenced code block, and the three fence ticks shifted
// every pair after them — so the check "passed" over a prompt that names three
// tools. That is the same shape as the defect under repair (a required field with
// no reachable way to fill it, a gate that reports on a lane it never read), so it
// gets held down rather than trusted.
//
// Three things are pinned: the extractor finds the names actually in the shipped
// prompt, the membership check BITES when the grant is missing them, and prose that
// merely contains a tool word is not mistaken for an order to use it.
func TestToolNameExtractionIsNotVacuous(t *testing.T) {
	named := toolsNamedIn(injectContent(t, "get-it-right.yaml", "attempt", "review"))
	require.NotEmpty(t, named,
		"the reviewer's prompt names shell-family tools; an extractor that returns nothing "+
			"makes TestReviewerHoldsEveryToolItsInstructionsName pass by asking nothing")
	for _, want := range []string{tools.ToolBashOutput, tools.ToolBashList, tools.ToolBashKill} {
		require.Contains(t, named, want,
			"the reviewer's prompt tells the agent to use %q, so extraction must see it", want)
	}

	t.Run("the membership check bites on a grant that lacks them", func(t *testing.T) {
		// The exact grant the measured run had, minus the shell family: this is the
		// state that produced the evidence-free `stuck`.
		crippled := make(map[string]bool)
		for _, n := range tools.ExpandToolFilter([]string{"view", "tag:web", "component_library"}, nil) {
			crippled[n] = true
		}
		for _, tool := range named {
			require.False(t, crippled[tool],
				"%q must NOT resolve from a grant without the shell family — if it does, the "+
					"assertion cannot distinguish a correct grant from the broken one", tool)
		}
	})

	t.Run("a tool word inside a longer phrase is not an order to use the tool", func(t *testing.T) {
		// `skill` and `view` are both registered tools. Neither of these is naming one.
		require.Empty(t, toolsNamedIn("run `reliant forge skill load frontend/design` first"),
			"a backticked shell command that happens to contain a tool word is not a tool order")
		require.Empty(t, toolsNamedIn("the schema requires `live_url` and `pages_inspected`"),
			"response-schema fields are not tools")
		require.Equal(t, []string{tools.ToolBashOutput}, toolsNamedIn("read it out of `bash_output`"),
			"a standalone backticked tool name IS a tool order")
	})
}

func defaultReviewTools(t *testing.T) []string {
	t.Helper()
	doc := loadWorkflowYAML(t, "get-it-right.yaml")
	inputs := mapAt(t, doc, "inputs")
	return stringSlice(t, mapAt(t, inputs, "review_tools")["default"])
}

func forgeOneShotArgs(t *testing.T) map[string]interface{} {
	t.Helper()
	doc := loadWorkflowYAML(t, "forge-one-shot.yaml")
	return mapAt(t, nodeByID(t, doc, "build_mvp"), "args")
}

func forgeOneShotReviewTools(t *testing.T) []string {
	t.Helper()
	return stringSlice(t, forgeOneShotArgs(t)["review_tools"])
}

func forgeOneShotReviewInstructions(t *testing.T) string {
	t.Helper()
	s, ok := forgeOneShotArgs(t)["review_instructions"].(string)
	require.True(t, ok, "build_mvp must set review_instructions")
	return s
}

// =============================================================================
// PROMPT RENDERING
// =============================================================================

func renderPrompt(t *testing.T, template string, inputs, nodes, outputs map[string]interface{}, iteration int) string {
	t.Helper()
	rendered, err := wfcel.EvaluateTemplate(template, &wfcel.EdgeEvalContext{
		Inputs:   inputs,
		Nodes:    nodes,
		Outputs:  outputs,
		Iter:     &model.IterContext{Iteration: iteration, Index: iteration},
		Workflow: &model.WorkflowContext{ID: "wf", Name: "get-it-right"},
	})
	require.NoError(t, err, "the prompt template must evaluate")
	s, ok := rendered.(string)
	require.True(t, ok, "a mixed template must render to a string, got %T", rendered)
	return s
}

func gateInputs() map[string]interface{} {
	return map[string]interface{}{
		"lint_command":        "task lint",
		"test_command":        "task test",
		"build_command":       "task build",
		"lint_log":            "./.reliant/data/get-it-right/lint.log",
		"test_log":            "./.reliant/data/get-it-right/test.log",
		"build_log":           "./.reliant/data/get-it-right/build.log",
		"max_retries":         5,
		"context_bridge":      "feedback_only",
		"implementer_preset":  "forge",
		"start_app_command":   "cd /srv/app && start-the-app",
		"review_instructions": "",
	}
}

// TestGetItRightRetryPromptTellsTheTruthAboutAGreenGate pins the retry prompt to
// what actually happened.
//
// The prompt hard-coded the gate-failure case on `iter.iteration > 0`, so a retry
// the REVIEWER asked for opened by announcing a failure that had not occurred and
// then listing, in its own next three lines, the three lanes that passed. Verbatim
// from attempt 2 of a measured run:
//
//	**The gate failed. Below is WHICH LANE failed, taken from the run itself.**
//	- lint — PASSED
//	- test — PASSED
//	- build — PASSED
//	READ THE FAILING LANE LOG FIRST, before you change anything…
//
// Sent to an agent that executes prose literally, this is worse than silence: the
// one instruction it can still follow is "read the failing lane log", and there is
// no failing lane, so the attempt spends its opening turns proving a negative
// instead of reading the reviewer's actual complaint. The branch is now taken from
// the `gate_failed` loop output, and on a green gate the prompt leads with the
// verdict that really did drive the retry.
func TestGetItRightRetryPromptTellsTheTruthAboutAGreenGate(t *testing.T) {
	template := injectContent(t, "get-it-right.yaml", "attempt", "implement")

	const reviewerVerdict = "The deviation-state page renders an empty table where it should render the terminal-state banner."

	t.Run("green gate leads with the reviewer, never with a failure", func(t *testing.T) {
		prompt := renderPrompt(t, template, gateInputs(), nil, map[string]interface{}{
			"lint_exit":       0,
			"test_exit":       0,
			"build_exit":      0,
			"gate_failed":     false,
			"review_feedback": reviewerVerdict,
		}, 1)

		require.NotContains(t, prompt, "The gate failed",
			"every configured lane exited 0, so the retry prompt must not open by announcing "+
				"a gate failure — it then lists those same three lanes as PASSED, and the agent "+
				"is left to reconcile the prompt against itself")
		require.NotContains(t, prompt, "READ THE FAILING LANE LOG FIRST",
			"there is no failing lane to read; this instruction sends the attempt hunting for "+
				"evidence that does not exist")
		require.NotContains(t, prompt, "reliant forge project audit",
			"the audit fallback hangs off the failing-lane branch — offering a diagnostic for a "+
				"failure that did not happen is what bought a measured run a 9-minute detour")
		require.Contains(t, prompt, "The gate is GREEN",
			"the prompt must say the gate passed, so the agent stops looking for a broken lane")
		require.Contains(t, prompt, reviewerVerdict,
			"on a green gate the reviewer's feedback IS the work; it must be in the prompt, not "+
				"referred to as something in the conversation above")
	})

	t.Run("red gate keeps the failing-lane instructions", func(t *testing.T) {
		prompt := renderPrompt(t, template, gateInputs(), nil, map[string]interface{}{
			"lint_exit":       0,
			"test_exit":       1,
			"build_exit":      0,
			"gate_failed":     true,
			"review_feedback": reviewerVerdict,
		}, 1)

		require.Contains(t, prompt, "The gate failed")
		require.Contains(t, prompt, "READ THE FAILING LANE LOG FIRST")
		require.Contains(t, prompt, "test — FAILED (exit 1)")
		require.Contains(t, prompt, "lint — PASSED")
		require.NotContains(t, prompt, "The gate is GREEN")
	})

	t.Run("first attempt reports no gate at all", func(t *testing.T) {
		prompt := renderPrompt(t, template, gateInputs(), nil, map[string]interface{}{}, 0)
		require.NotContains(t, prompt, "The gate failed")
		require.NotContains(t, prompt, "The gate is GREEN")
	})
}

// TestGetItRightHandsTheReviewerTheStartCommandInsteadOfAPort holds the app-start
// seam to the one thing it must never do again: describe an app nobody started.
//
// `start_app_command` was a DEAD INPUT. forge-one-shot set it to
// `cd … && reliant forge run`; its only consumer in the whole tree was one
// sentence in this inject; no node executed it, and a measured run recorded zero
// rows in `background_processes`. That sentence nevertheless told the reviewer
// "The application was started via the phase start command (local port 3000)" —
// false on both halves, since the run's real ports were ephemeral
// (`[up] host peptidesadmin: ephemeral dev port 58934`).
//
// A declared-but-unexecuted input is the worst of the available options: it costs
// a caller the work of setting it, and it pays back a false statement. The seam is
// now that the reviewer starts the app itself, with the shell family — the machinery
// that actually owns process lifecycle — and the command is interpolated into its
// prompt verbatim rather than paraphrased.
func TestGetItRightHandsTheReviewerTheStartCommandInsteadOfAPort(t *testing.T) {
	reviewInject := injectContent(t, "get-it-right.yaml", "attempt", "review")

	require.NotContains(t, reviewInject, "The application was started via the phase start command",
		"nothing in this workflow starts the application; the reviewer must be told to start "+
			"it, not told that it is already up")
	require.Contains(t, reviewInject, "inputs.start_app_command",
		"the reviewer's prompt must interpolate the start command itself — an input whose only "+
			"consumer is a sentence ABOUT it is a dead input")

	t.Run("the rendered prompt carries the command verbatim", func(t *testing.T) {
		const cmd = "cd /srv/app && reliant forge env down dev; reliant forge run"
		inputs := gateInputs()
		inputs["start_app_command"] = cmd
		prompt := renderPrompt(t, reviewInject, inputs,
			map[string]interface{}{
				"lint":  map[string]interface{}{"exit_code": 0},
				"test":  map[string]interface{}{"exit_code": 0},
				"build": map[string]interface{}{"exit_code": 0},
			},
			map[string]interface{}{}, 0)
		require.Contains(t, prompt, cmd,
			"the reviewer cannot run a command it was never given")
		require.NotContains(t, prompt, "was started",
			"the prompt must not claim the app is already running")
	})

	t.Run("no start command means no start instructions", func(t *testing.T) {
		inputs := gateInputs()
		inputs["start_app_command"] = ""
		prompt := renderPrompt(t, reviewInject, inputs,
			map[string]interface{}{
				"lint":  map[string]interface{}{"exit_code": 0},
				"test":  map[string]interface{}{"exit_code": 0},
				"build": map[string]interface{}{"exit_code": 0},
			},
			map[string]interface{}{}, 0)
		require.NotContains(t, prompt, "Bring the app up",
			"a caller with no app to show must not be told to start one")
	})

	// A nominal port is a fact-shaped guess. forge-one-shot's carried the comment
	// "NOMINAL ONLY — almost never the real one" and was still interpolated into a
	// sentence an agent reads as fact; a guessed port that answers is more likely to
	// be an unrelated server than the app under review, and a false green costs more
	// than a failure. There is nothing left to derive it from, so it is gone.
	t.Run("no workflow declares or passes a nominal app port", func(t *testing.T) {
		entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
		require.NoError(t, err)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
				continue
			}
			data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
			require.NoError(t, err)
			require.NotContains(t, string(data), "app_port",
				"%s still carries app_port: dev servers are assigned their port at launch, so a "+
					"port written into a workflow is a guess that reads as fact. The reviewer "+
					"reads the port off what the server printed.", entry.Name())
		}
	})
}

// TestGetItRightReEntersAtReviewWhenTheREVIEWIsWhatWasStuck pins the second door
// into the loop, and pins that it did not replace the first one.
//
// `stuck` means the REVIEW could not be performed — a tool the reviewer was not
// granted, a URL it could not obtain. The human's answer is therefore addressed to
// the reviewer, but `stuck` + feedback re-entered the loop at its entry, which is
// `implement`. Measured: the human replied "Nothing is broken… do not redo the
// implementation. Just run the review properly this time" and attempt 2 spent
// 9m04s and 31 LLM calls making zero edits, because an agent holding write tools
// and no way to file a verdict was the only thing the workflow could hand it to.
//
// The fix is a condition on `implement`, not a new entry: entry stays `[implement]`
// for every other path, and the gate deliberately keeps running on the re-review
// iteration because "I fixed it, continue" is one of the human's options at that
// checkpoint. The end-to-end behaviour is held by the
// stuck_feedback_rereviews_without_reimplementing scenario; this holds the shape
// the scenario depends on.
func TestGetItRightReEntersAtReviewWhenTheREVIEWIsWhatWasStuck(t *testing.T) {
	doc := loadWorkflowYAML(t, "get-it-right.yaml")
	inline := mapAt(t, nodeByID(t, doc, "attempt"), "inline")

	require.Equal(t, []string{"implement"}, stringSlice(t, inline["entry"]),
		"the normal path must still enter at implement — gate-failure retries depend on it")

	implement := nodeByID(t, inline, "implement")
	condition, ok := implement["condition"].(string)
	require.True(t, ok, "implement must carry the re-review condition")

	// The condition is the whole mechanism, so evaluate it rather than grep it.
	for _, tc := range []struct {
		name    string
		outputs map[string]interface{}
		run     bool
	}{
		{"first iteration", map[string]interface{}{}, true},
		{"reviewer wants changes", map[string]interface{}{"eval_strategy": "continue", "has_feedback": false}, true},
		{"gate failed", map[string]interface{}{"eval_strategy": "continue", "has_feedback": false, "gate_failed": true}, true},
		{"human commented after a non-stuck review", map[string]interface{}{"eval_strategy": "continue", "has_feedback": true}, true},
		{"stuck with no answer yet", map[string]interface{}{"eval_strategy": "stuck", "has_feedback": false}, true},
		{"stuck and the human answered", map[string]interface{}{"eval_strategy": "stuck", "has_feedback": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shouldRun, err := wfcel.EvaluateBool(condition, &wfcel.EdgeEvalContext{
				Outputs:  tc.outputs,
				Inputs:   gateInputs(),
				Iter:     &model.IterContext{Iteration: 1, Index: 1},
				Workflow: &model.WorkflowContext{ID: "wf", Name: "get-it-right"},
			})
			require.NoError(t, err)
			require.Equal(t, tc.run, shouldRun,
				"implement should run=%v when the previous iteration ended %v", tc.run, tc.outputs)
		})
	}

	// The reviewer must be told WHY there is no new diff, and be handed the human's
	// words. "See the conversation above" is a pointer the reviewer cannot follow
	// when context_bridge is 'none'.
	reviewInject := injectContent(t, "get-it-right.yaml", "attempt", "review")
	require.Contains(t, reviewInject, "outputs.stuck_feedback",
		"the re-review prompt must quote what the human actually typed")

	prompt := renderPrompt(t, reviewInject, gateInputs(),
		map[string]interface{}{
			"lint":  map[string]interface{}{"exit_code": 0},
			"test":  map[string]interface{}{"exit_code": 0},
			"build": map[string]interface{}{"exit_code": 0},
		},
		map[string]interface{}{
			"eval_strategy":  "stuck",
			"has_feedback":   true,
			"stuck_feedback": "Nothing is broken. Just run the review properly this time.",
		}, 1)
	require.Contains(t, prompt, "Nothing is broken. Just run the review properly this time.")
	require.Contains(t, prompt, "Nothing was re-implemented",
		"a reviewer re-reading identical code must be told it is identical, or it goes looking "+
			"for a diff that does not exist")
}

// TestShellTagCarriesTheToolsTheShellToolTellsAgentsToUse closes the drift at its
// source rather than at each prompt that trips over it.
//
// The shell tool's own description says: "Use 'run_in_background: true' to run
// long-running commands in the background. You can then use BashOutput to check
// output, BashKill to terminate, and BashList to see all running processes."
// Granting `tag:shell` without those three therefore hands every holder — ux,
// tester, refactor, debug, code_reviewer, planner, researcher, git, documentation,
// and plan mode — instructions for tools it does not have. They are also useless
// without the shell: nothing can be in `bash_list` for an agent that cannot start
// a process.
func TestShellTagCarriesTheToolsTheShellToolTellsAgentsToUse(t *testing.T) {
	granted := make(map[string]bool)
	for _, name := range tools.ExpandToolFilter([]string{"tag:shell"}, nil) {
		granted[name] = true
	}

	for _, companion := range []string{tools.ShellToolName, tools.ToolBashOutput, tools.ToolBashList, tools.ToolBashKill} {
		require.True(t, granted[companion],
			"tag:shell must resolve to the whole shell family; %q is missing, so an agent told "+
				"by the shell tool's own description to use it has no such tool", companion)
	}

	// The inverse has to hold too, or `!tag:shell` stops meaning "no shell".
	excluded := tools.ExpandToolFilter([]string{"tag:default", "!tag:shell"}, nil)
	for _, name := range excluded {
		require.NotContains(t, []string{tools.ShellToolName, tools.ToolBashOutput, tools.ToolBashList, tools.ToolBashKill}, name,
			fmt.Sprintf("!tag:shell must remove the whole family, but %q survived", name))
	}
}
