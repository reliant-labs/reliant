// Copyright (c) 2025 Reliant Labs

// Package lint is the authoring-time review of a workflow definition that
// already validates. Validation (internal/workflow/validation) answers "can
// this run?" and is enforced wherever a definition is saved or started. Lint
// answers "will it behave when nobody is watching?": does every run end, does
// a failure reach someone, can a run hold a capacity slot forever, do two
// steps write one checkout, is a secret pasted into the file, do the
// scenarios cover every way the workflow starts and ends.
//
// Lint runs only where a definition is written and reviewed (`reliant
// workflow lint`, `reliant workflow validate`, and so every repository's merge
// gate), never at save or start: a rule added in a later release must not
// make a stored automation refuse to fire.
//
// Every rule has a stable ID, a severity and a section in the docs
// (DocsURL). An error blocks the gate; a warning is reported. A finding the
// author has judged is silenced in the YAML, next to the node, with a reason:
//
//	# lint:allow action-error-unread — best effort: a 404 means already removed
//	- id: clear_label
//
//forge:exclude-contract: pure analysis of a parsed definition, its source and its scenarios; no I/O, state or clock, and nothing to substitute
package lint

import (
	"fmt"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
)

// Severity is what a finding does to the gate.
type Severity string

const (
	// SeverityError blocks: `reliant workflow validate` and `lint` exit 1.
	SeverityError Severity = "error"
	// SeverityWarning is reported and does not block.
	SeverityWarning Severity = "warning"
)

// DocsBase is the page every rule's section lives on.
const DocsBase = "https://docs.reliantlabs.io/workflows/lint"

// Finding is one rule's verdict on one place in a definition.
type Finding struct {
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	// Node is the node path the finding is about ("gate", "loop.call_llm",
	// "on_failure.notify"); empty for the workflow as a whole.
	Node    string `json:"node,omitempty"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
	Docs    string `json:"docs"`
}

// String is the finding as one line: `[rule] node: message (fix) docs`.
func (f Finding) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] ", f.Rule)
	if f.Node != "" {
		sb.WriteString(f.Node)
		sb.WriteString(": ")
	}
	sb.WriteString(f.Message)
	if f.Fix != "" {
		fmt.Fprintf(&sb, " (%s)", f.Fix)
	}
	fmt.Fprintf(&sb, " %s", f.Docs)
	return sb.String()
}

// Rule describes one check.
type Rule struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Summary  string   `json:"summary"`
	Docs     string   `json:"docs"`
}

// Rule IDs. Stable: an ID is what a `lint:allow` comment and the docs name,
// so a rule is renamed only with every allowance that names it.
const (
	RuleRunNoTimeout           = "run-no-timeout"
	RuleRunExitCodeUnread      = "run-exit-code-unread"
	RuleExitCodeUnhandled      = "exit-code-unhandled"
	RuleEdgeNoDefault          = "edge-no-default"
	RuleActionErrorUnread      = "action-error-unread"
	RuleNoFailurePath          = "no-failure-path"
	RuleAgentTurnBudget        = "agent-turn-budget-default"
	RuleLoopUnbounded          = "loop-unbounded"
	RuleAgentDecodeFreeText    = "agent-decode-free-text"
	RuleSlotUnboundedWait      = "slot-unbounded-wait"
	RuleSharedCheckoutFetch    = "shared-checkout-fetch"
	RuleSecretLiteral          = "secret-literal"
	RuleAutomationUntriggered  = "automation-only-untriggered"
	RuleRequiresUndeclaredTool = "requires-undeclared-tool"
	RuleTimeoutIgnored         = "timeout-ignored"
	RuleScenarioNone           = "scenario-none"
	RuleScenarioTrigger        = "scenario-trigger-uncovered"
	RuleScenarioTerminal       = "scenario-terminal-uncovered"
	RuleAllowInvalid           = "lint-allow-invalid"
)

func rule(id string, sev Severity, summary string) Rule {
	return Rule{ID: id, Severity: sev, Summary: summary, Docs: docsFor(id)}
}

var rules = []Rule{
	rule(RuleTimeoutIgnored, SeverityWarning, "A workflow or loop node declares timeout:, which bounds nothing: a node's timeout applies to the activity it dispatches, and these dispatch none."),
	rule(RuleRunNoTimeout, SeverityWarning, "A run node has no timeout, so a hung command runs for the 30-day activity default."),
	rule(RuleRunExitCodeUnread, SeverityWarning, "Nothing reads a run node's exit_code, so a failed command looks like a successful one."),
	rule(RuleExitCodeUnhandled, SeverityError, "An edge branches on a run node's exit_code and has no default, so an exit code no case names ends the run silently."),
	rule(RuleEdgeNoDefault, SeverityWarning, "An edge has cases and no default: when no case matches, that branch of the run ends silently."),
	rule(RuleActionErrorUnread, SeverityWarning, "Nothing reads an action node's is_error, so a failed call (a 4xx, a revoked credential) looks like a successful one."),
	rule(RuleNoFailurePath, SeverityWarning, "A triggered workflow has no on_failure handler, so an unattended run that fails tells nobody."),
	rule(RuleAgentTurnBudget, SeverityWarning, "An agent node leaves the child's max_turns at its default: the turn budget is a decision, not an accident."),
	rule(RuleLoopUnbounded, SeverityWarning, "A loop's while never reads iter.iteration and the loop has no items, so nothing bounds it."),
	rule(RuleAgentDecodeFreeText, SeverityError, "An expression json.decode()s an agent's free text; agents have a structured-output contract (response_schema) for that."),
	rule(RuleSlotUnboundedWait, SeverityError, "A workflow with concurrency.max_running has a step (its own, or a child's) that can run without bound (a run node with no timeout, a loop with no bound), so one run can hold a slot forever."),
	rule(RuleSharedCheckoutFetch, SeverityError, "A run step fetches into the shared project checkout's own refs or tags, racing every other step that does (a warning when the directory is not provably the checkout)."),
	rule(RuleSecretLiteral, SeverityError, "A secret is written into the definition instead of being read through a credential."),
	rule(RuleAutomationUntriggered, SeverityWarning, "automation_only: true with no triggers: no chat can start it and nothing else will."),
	rule(RuleRequiresUndeclaredTool, SeverityWarning, "A triggered workflow's command calls an external tool (gh, task, jq, …) its requires.tools does not list, so preflight cannot check it."),
	rule(RuleScenarioNone, SeverityWarning, "The workflow has no scenarios."),
	rule(RuleScenarioTrigger, SeverityWarning, "No scenario fires the workflow through this trigger (scenario `trigger:`)."),
	rule(RuleScenarioTerminal, SeverityWarning, "No scenario expects the run to reach this terminal node."),
	rule(RuleAllowInvalid, SeverityError, "A `lint:allow` comment names an unknown rule or gives no reason."),
}

// Rules lists every rule, in documentation order.
func Rules() []Rule { return append([]Rule(nil), rules...) }

func docsFor(id string) string { return DocsBase + "#" + id }

func ruleSeverity(id string) (Severity, bool) {
	for _, r := range rules {
		if r.ID == id {
			return r.Severity, true
		}
	}
	return "", false
}

// Input is one definition to lint.
type Input struct {
	Workflow *reliantv1.Workflow
	// Source is the YAML the definition was parsed from; its `lint:allow`
	// comments are read from it. Nil reads none.
	Source []byte
	// Scenarios are the workflow's scenarios; none reports scenario-none.
	Scenarios []*scenario.Scenario
	// LoadRef resolves a `ref:` (builtin://autopilot-agent, project://x) so
	// rules can read the child's inputs. Nil, or an error, skips the rules
	// that need the child.
	LoadRef func(ref string) (*reliantv1.Workflow, error)
}

// Check runs every rule over one definition and returns the findings that
// no `lint:allow` silences, sorted by severity, then node, then rule.
func Check(in Input) []Finding {
	wf := in.Workflow
	if wf == nil {
		return nil
	}
	c := &checker{in: in, wf: wf, reads: newReadIndex(wf)}
	c.run()
	allowed, problems := parseAllowances(in.Source)
	var out []Finding
	for _, f := range c.findings {
		if allowed.covers(f) {
			continue
		}
		out = append(out, f)
	}
	out = append(out, problems...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == SeverityError
		}
		if out[i].Node != out[j].Node {
			return out[i].Node < out[j].Node
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

// HasErrors reports whether any finding blocks.
func HasErrors(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

type checker struct {
	in       Input
	wf       *reliantv1.Workflow
	reads    *readIndex
	findings []Finding
}

// add records a finding at the rule's own severity.
func (c *checker) add(rule, node, message, fix string) {
	sev, _ := ruleSeverity(rule)
	c.addAt(sev, rule, node, message, fix)
}

func (c *checker) addAt(sev Severity, rule, node, message, fix string) {
	c.findings = append(c.findings, Finding{Rule: rule, Severity: sev, Node: node, Message: message, Fix: fix, Docs: docsFor(rule)})
}

func (c *checker) run() {
	slotBound := c.wf.GetConcurrency().GetMaxRunning() > 0
	for _, g := range graphsOf(c.wf) {
		c.checkGraph(g, slotBound)
	}
	c.checkWorkflow()
	c.checkSecrets()
	c.checkScenarios()
}
