// Copyright (c) 2025 Reliant Labs
package lint

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// checkGraph runs the per-node and per-edge rules over one graph. slotBound
// is whether the workflow declares concurrency.max_running: then every wait
// without a bound is an error, because the run holds a slot while it waits.
func (c *checker) checkGraph(g graph, slotBound bool) {
	byID := map[string]*reliantv1.Node{}
	for _, n := range g.wf.GetNodes() {
		byID[n.GetId()] = n
	}
	for _, n := range g.wf.GetNodes() {
		id, path := n.GetId(), g.prefix+n.GetId()
		c.checkMachinePin(path, n.GetDaemon())
		timed := model.CelStringIsSet(n.GetTimeout())
		switch n.GetType() {
		case model.NodeTypeRun:
			if !timed {
				if slotBound {
					c.add(RuleSlotUnboundedWait, path, "run node has no timeout; a hung command holds this workflow's max_running slot for the 30-day activity default",
						"set timeout: to the longest the command may take")
				} else {
					c.add(RuleRunNoTimeout, path, "run node has no timeout; a hung command runs for the 30-day activity default",
						"set timeout: to the longest the command may take")
				}
			}
			cmd := model.CelStringRaw(model.GetRunArgs(n).GetCommand())
			c.checkUndeclaredTools(path, cmd)
			if model.TargetsApprovalDecision(cmd) {
				c.addApprovalDecider(path)
			}
			if !g.onFailure && !trivialCommand(cmd) && !c.reads.reads(id, "exit_code") {
				c.add(RuleRunExitCodeUnread, path, "nothing reads this node's exit_code, so a failed command routes like a successful one",
					fmt.Sprintf("branch on nodes.%s.exit_code (an edge case, or a condition downstream)", id))
			}
			c.checkFetch(path, n, cmd)
			c.checkCredentialsNoneSignIns(path, cmd, model.GetRunArgs(n).GetCredentials())
		case model.NodeTypeAskQuestion:
			c.checkAskQuestionType(path, n)
		case model.NodeTypeAction:
			c.checkApprovalDecider(path, n)
			if !g.onFailure && !c.reads.reads(id, "is_error") {
				c.add(RuleActionErrorUnread, path, "nothing reads this node's is_error, so a failed call routes like a successful one",
					fmt.Sprintf("branch on nodes.%s.is_error, or allow it with the reason the failure does not matter", id))
			}
		case model.NodeTypeWorkflow:
			if timed {
				c.add(RuleTimeoutIgnored, path, "timeout: on a workflow node bounds nothing: the runtime applies a node's timeout to the activity the node dispatches, and a workflow node dispatches none (its child's nodes run on their own timeouts)",
					"remove it, and bound the child's own steps (timeouts on its run nodes, args.max_turns on an agent)")
			}
			if slotBound {
				c.checkChildBounded(path, n)
			}
			c.checkTurnBudget(path, n)
		case model.NodeTypeReview:
			if timed {
				c.add(RuleTimeoutIgnored, path, "timeout: on a review node bounds nothing: it runs as a workflow node, which dispatches no activity of its own",
					"remove it; a reviewer is bounded by max_turns (default 100)")
			}
		case model.NodeTypeLoop:
			if timed {
				c.add(RuleTimeoutIgnored, path, "timeout: on a loop node bounds nothing: the runtime applies a node's timeout to the activity the node dispatches, and a loop node dispatches none",
					"remove it, and bound the loop with iter.iteration in while")
			}
			if slotBound {
				c.checkChildBounded(path, n)
			}
			la := model.GetLoopArgs(n)
			while := model.DirectCelExpr(la.GetWhile())
			if !model.CelStringIsSet(la.GetItems()) && !strings.Contains(while, "iteration") {
				msg := "the loop's while never reads iter.iteration and it has no items, so nothing bounds it"
				fix := "add `&& iter.iteration < N` to while"
				if slotBound {
					c.add(RuleSlotUnboundedWait, path, msg+"; it can hold this workflow's max_running slot forever", fix)
				} else {
					c.add(RuleLoopUnbounded, path, msg, fix)
				}
			}
			c.checkTurnBudget(path, n)
		}
	}

	for _, e := range g.wf.GetEdges() {
		if len(e.GetCases()) == 0 || len(e.GetDefault()) > 0 || exhaustiveCases(e.GetCases()) {
			continue
		}
		path := g.prefix + e.GetFrom()
		src := byID[e.GetFrom()]
		onExit := false
		if src != nil && src.GetType() == model.NodeTypeRun {
			re := nodeFieldPattern(e.GetFrom(), "exit_code")
			for _, cs := range e.GetCases() {
				if re.MatchString(cs.GetCondition()) {
					onExit = true
					break
				}
			}
		}
		if onExit {
			c.add(RuleExitCodeUnhandled, path, "this edge branches on the command's exit_code and has no default, so any other exit code ends the run here, silently",
				"add a default: that reports the unexpected exit code")
		} else {
			c.add(RuleEdgeNoDefault, path, "this edge has cases and no default; when none matches, this branch of the run ends silently",
				"add a default:, even one that only saves a message")
		}
	}
}

// checkChildBounded: a slot-bounded workflow's child holds the slot for as
// long as it runs, so the child's own waits must be bounded too. It is read
// through LoadRef, every ref it reaches in turn; a ref that does not resolve
// statically (a template) or a loader error is skipped, never guessed.
//
// A child loop bounded by one of its own inputs, where 0 means no limit
// (builtin://agent's `inputs.max_turns == 0 || iter.iteration <
// inputs.max_turns`), is bounded only when the node that runs it sets that
// input. Left unset, it is reported as a warning naming the arg to set: the
// error this rule gives an unbounded loop predates such inputs, and a stricter
// verdict warns before it gates.
func (c *checker) checkChildBounded(path string, node *reliantv1.Node) {
	ref := childRef(node)
	if c.in.LoadRef == nil || ref == "" || strings.Contains(ref, "{{") {
		return
	}
	visited := map[string]bool{}
	var walk func(ref, via string, caller *reliantv1.Node)
	walk = func(ref, via string, caller *reliantv1.Node) {
		if visited[ref] {
			return
		}
		visited[ref] = true
		child, err := c.in.LoadRef(ref)
		if err != nil || child == nil {
			return
		}
		for _, g := range graphsOf(child) {
			for _, n := range g.wf.GetNodes() {
				where := via + " → " + g.prefix + n.GetId()
				// Approvals and questions are not here: max_running bounds only
				// trigger-launched runs, which are unattended, and an
				// unattended run's human gates resolve at once.
				switch n.GetType() {
				case model.NodeTypeRun:
					if !model.CelStringIsSet(n.GetTimeout()) {
						c.add(RuleSlotUnboundedWait, path, fmt.Sprintf("the child's run node %s has no timeout, and this workflow's max_running slot is held while it runs", where),
							"give that node a timeout (in the child), or run the child outside the slot-bounded workflow")
					}
				case model.NodeTypeLoop:
					la := model.GetLoopArgs(n)
					if model.CelStringIsSet(la.GetItems()) {
						break
					}
					unconditional, optIn := loopBound(model.DirectCelExpr(la.GetWhile()))
					switch {
					case unconditional:
					case len(optIn) > 0:
						if !setsAnyNonZero(caller, optIn) && !defaultsAnyNonZero(child, optIn) {
							c.addAt(SeverityWarning, RuleSlotUnboundedWait, path, fmt.Sprintf("the child's loop %s is bounded only by its %s input, which this node leaves at 0 (no limit), holding this workflow's max_running slot", where, strings.Join(optIn, "/")),
								fmt.Sprintf("set args.%s to the budget this task needs", optIn[0]))
						}
					default:
						c.add(RuleSlotUnboundedWait, path, fmt.Sprintf("the child's loop %s has no iteration bound, holding this workflow's max_running slot", where),
							"bound it with iter.iteration in while")
					}
				}
				if r := childRef(n); r != "" && !strings.Contains(r, "{{") {
					walk(r, where, n)
				}
			}
		}
	}
	walk(ref, ref, node)
}

// optInBoundPattern is a loop bound its workflow's caller switches on: `inputs.X
// == 0 || iter.iteration < inputs.X`, where X = 0 means no limit. Both names
// are captured (Go regexp has no backreference) and must agree.
var optInBoundPattern = regexp.MustCompile(`\(?\s*inputs\.(\w+)\s*==\s*0\s*\|\|\s*iter\.iteration\s*<=?\s*inputs\.(\w+)\s*\)?`)

// loopBound reads a while condition: unconditional when iter.iteration bounds
// it whatever the inputs, else the inputs that bound it once a caller sets one
// to a non-zero value (none: nothing bounds it).
func loopBound(while string) (unconditional bool, optIn []string) {
	rest := optInBoundPattern.ReplaceAllStringFunc(while, func(m string) string {
		sub := optInBoundPattern.FindStringSubmatch(m)
		if sub[1] != sub[2] {
			return m
		}
		optIn = append(optIn, sub[1])
		return ""
	})
	return strings.Contains(rest, "iteration"), optIn
}

// defaultsAnyNonZero reports whether wf declares one of names with a non-zero
// default: a caller that leaves it unset then runs the loop bounded by that
// default, as builtin://agent's max_turns (default 0) is not.
func defaultsAnyNonZero(wf *reliantv1.Workflow, names []string) bool {
	for _, name := range names {
		in := wf.GetInputs()[name]
		if in.GetIntegerInput().GetDefault() != 0 || in.GetNumberInput().GetDefault() != 0 {
			return true
		}
	}
	return false
}

// setsAnyNonZero reports whether a workflow or loop node gives its child one of
// names: an arg that is not the literal 0, or a passthrough of that name. A
// template counts as set; lint cannot know its value.
func setsAnyNonZero(n *reliantv1.Node, names []string) bool {
	var args map[string]*structpb.Value
	var passthrough []string
	if swa := model.GetSubWorkflowArgs(n); swa != nil {
		args, passthrough = swa.GetArgs(), swa.GetPassthrough()
	} else if la := model.GetLoopArgs(n); la != nil {
		args = la.GetArgs()
	}
	for _, name := range names {
		if slices.Contains(passthrough, name) {
			return true
		}
		v, ok := args[name]
		if !ok {
			continue
		}
		switch k := v.GetKind().(type) {
		case *structpb.Value_NumberValue:
			if k.NumberValue != 0 {
				return true
			}
		case *structpb.Value_StringValue:
			if s := strings.TrimSpace(k.StringValue); s != "" && s != "0" {
				return true
			}
		}
	}
	return false
}

// comparisonPattern is `<operand> == <value>` or `<operand> != <value>`, the
// whole condition.
var comparisonPattern = regexp.MustCompile(`^\s*\(?\s*(.+?)\s*(==|!=)\s*(.+?)\s*\)?\s*$`)

// exhaustiveCases reports whether an edge's cases provably cover every
// value, so no default is needed: one case is literally `true`, or two cases
// are complements (`x == v` and `x != v`; `c` and `!c`).
func exhaustiveCases(cases []*reliantv1.EdgeCase) bool {
	norm := func(s string) string { return strings.Join(strings.Fields(s), "") }
	seen := map[string]bool{}
	for _, cs := range cases {
		cond := norm(cs.GetCondition())
		if cond == "true" {
			return true
		}
		seen[cond] = true
	}
	for cond := range seen {
		if seen["!"+cond] || seen["!("+cond+")"] {
			return true
		}
		if m := comparisonPattern.FindStringSubmatch(cond); m != nil {
			flip := "!="
			if m[2] == "!=" {
				flip = "=="
			}
			if seen[m[1]+flip+m[3]] {
				return true
			}
		}
	}
	return false
}

// trivialCommand is a command whose failure means nothing: a fixed sleep (a
// backoff) or a no-op.
var trivialCommandPattern = regexp.MustCompile(`^\s*(sleep\s+[0-9.]+[smhd]?|true|:)\s*$`)

func trivialCommand(cmd string) bool { return trivialCommandPattern.MatchString(cmd) }

// checkTurnBudget: an agent child (one that declares max_turns) whose node
// leaves it unset runs on the child's default. A default of 0 is no limit
// (builtin://agent, builtin://structured-agent): such an agent never runs out
// of turns, so leaving it is not this finding (an unattended workflow that
// needs the bound is told by slot-unbounded-wait).
func (c *checker) checkTurnBudget(path string, n *reliantv1.Node) {
	ref := childRef(n)
	if ref == "" || strings.Contains(ref, "{{") || c.in.LoadRef == nil {
		return
	}
	child, err := c.in.LoadRef(ref)
	if err != nil || child == nil {
		return
	}
	in, ok := child.GetInputs()["max_turns"]
	if !ok {
		return
	}
	var args map[string]bool
	if swa := model.GetSubWorkflowArgs(n); swa != nil {
		args = keysOf(swa.GetArgs())
	} else if la := model.GetLoopArgs(n); la != nil {
		args = keysOf(la.GetArgs())
	}
	if args["max_turns"] {
		return
	}
	d := in.GetIntegerInput().GetDefault()
	if d == 0 {
		return
	}
	c.add(RuleAgentTurnBudget, path, fmt.Sprintf("the agent runs on %s's default max_turns (%d); an agent that runs out of turns submits nothing", ref, d),
		"set args.max_turns to the budget this task needs, and say it in the prompt")
}

func keysOf[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// gitFetchPattern is a git invocation that fetches or pulls.
var gitFetchPattern = regexp.MustCompile(`\bgit\b[^|;&\n]*?\s(fetch|pull)\b[^|;&\n]*`)

// checkFetch: a fetch into the checkout the project's chats and every other
// run share updates its remote-tracking refs (and tags) under them. Two
// writers race ("cannot lock ref"); a tag moved upstream fails every later
// fetch ("would clobber existing tag"). Safe: fetch into private refs
// (--refmap= with an explicit refs/<private>/ destination) with --no-tags, or
// into a private clone.
func (c *checker) checkFetch(path string, n *reliantv1.Node, cmd string) {
	run := model.GetRunArgs(n)
	changesDir := model.CelStringIsSet(run.GetWorkDir()) || regexp.MustCompile(`(^|[\s;&(])cd\s`).MatchString(cmd)
	for _, line := range strings.Split(joinContinuations(cmd), "\n") {
		for _, loc := range gitFetchPattern.FindAllStringSubmatchIndex(line, -1) {
			if insideQuotes(line[:loc[0]]) {
				continue // text being printed (`echo "git fetch failed"`), not a command
			}
			m := []string{line[loc[0]:loc[1]], line[loc[2]:loc[3]]}
			inv := m[0]
			if m[1] == "fetch" && strings.Contains(inv, "--refmap=") && strings.Contains(inv, "--no-tags") {
				continue
			}
			what := "updates the shared checkout's remote-tracking refs and tags"
			if m[1] == "pull" {
				what = "moves the shared checkout's branch, refs and tags"
			}
			fix := "fetch into this run's private refs: git fetch --no-tags --refmap= origin +refs/heads/main:refs/pipeline/<run>/main; or clone privately"
			if !changesDir && !strings.Contains(inv, " -C ") {
				c.add(RuleSharedCheckoutFetch, path, fmt.Sprintf("`%s` runs in the project checkout and %s, racing every other run that does", strings.TrimSpace(inv), what), fix)
			} else {
				c.addAt(SeverityWarning, RuleSharedCheckoutFetch, path, fmt.Sprintf("`%s` %s if its directory is the shared checkout", strings.TrimSpace(inv), what), fix+"; allow it with the reason if the directory is private")
			}
		}
	}
}

// checkApprovalDecider: an http/request@1 step aimed at ApprovalService's
// approve or deny rpcs. The credential it carries is opaque here, so the
// target is the signal; any Reliant token that permits approvals:decide
// (reliant:api included) can decide.
func (c *checker) checkApprovalDecider(path string, n *reliantv1.Node) {
	a := n.GetAction()
	if a == nil || strings.TrimSpace(model.CelStringRaw(a.GetUses())) != "http/request@1" {
		return
	}
	if model.TargetsApprovalDecision(model.ActionURL(a)) {
		c.addApprovalDecider(path)
	}
}

// addApprovalDecider reports a step that decides approvals. Once the workflow
// declares permissions: [approvals:decide] the steps are covered and the
// declaration itself is reported once (checkApprovalPermission).
func (c *checker) addApprovalDecider(path string) {
	if model.DeclaresPermission(c.wf, model.PermissionApprovalsDecide) {
		return
	}
	c.add(RuleApprovalDecider, path, "this step calls Reliant's ApprovalService to approve or deny, so the workflow can decide approvals, including its own run's, with the token it holds",
		"declare it with permissions: [approvals:decide] (hosted credentials that can decide approvals are refused without it), or remove the step; allow this finding with the reason if it is intended")
}

// checkApprovalPermission: the declaration is the grant of that power, so it
// is a finding in itself, allowed at the permissions: key with the reason.
func (c *checker) checkApprovalPermission() {
	if model.DeclaresPermission(c.wf, model.PermissionApprovalsDecide) {
		c.add(RuleApprovalDecider, "", "the workflow declares permissions: [approvals:decide]: it can approve or deny requests, including the ones its own run raises",
			"allow it with the reason on the permissions: key if this is intended")
	}
}

func joinContinuations(s string) string { return strings.ReplaceAll(s, "\\\n", " ") }

// externalTools are executables a machine image may or may not carry. A
// shell builtin or a coreutil every machine has is not worth declaring.
var externalTools = []string{"gh", "git", "task", "jq", "yq", "forge", "reliant", "docker", "kubectl", "helm", "gcloud", "aws", "go", "node", "npm", "npx", "pnpm", "python3", "buf", "k3d", "kcl", "terraform", "curl", "flock"}

var toolInvocation = regexp.MustCompile(`(?:^|[;&|({]\s*|\$\(\s*|\b(?:then|do|if|exec|timeout\s+\S+|!)\s+)(` + strings.Join(externalTools, "|") + `)(?:\s|$|\))`)

// checkUndeclaredTools: a triggered (unattended) workflow's command that calls
// an external tool its requires.tools does not list fails midway on a machine
// without it, instead of at preflight.
func (c *checker) checkUndeclaredTools(path, cmd string) {
	if len(c.wf.GetTriggers()) == 0 {
		return
	}
	declared := map[string]bool{}
	for _, t := range c.wf.GetRequires().GetTools() {
		declared[t] = true
	}
	var missing []string
	seen := map[string]bool{}
	for _, line := range strings.Split(joinContinuations(cmd), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, loc := range toolInvocation.FindAllStringSubmatchIndex(line, -1) {
			tool := line[loc[2]:loc[3]]
			if declared[tool] || seen[tool] || insideQuotes(line[:loc[2]]) {
				continue
			}
			seen[tool] = true
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		c.add(RuleRequiresUndeclaredTool, path, fmt.Sprintf("the command calls %s, which requires.tools does not list, so a machine without it fails this step midway instead of at preflight", strings.Join(missing, ", ")),
			"add them to requires.tools: ["+strings.Join(missing, ", ")+"]")
	}
}

// insideQuotes reports whether a shell line that starts with prefix is, at
// its end, inside a single- or double-quoted string.
func insideQuotes(prefix string) bool {
	var quote rune
	escaped := false
	for _, r := range prefix {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote == 0 && (r == '"' || r == '\''):
			quote = r
		case r == quote:
			quote = 0
		}
	}
	return quote != 0
}

// checkMachinePin: a literal selector that names one machine (by id, by name,
// or by the string shorthand that is not local/cloud/any) makes that machine
// the only one the run or node can use. A pool or labels choose among
// machines; a type, or a CEL selector, names none. The fix is a pool: it is
// placed once on whichever member is free.
func (c *checker) checkMachinePin(path string, sel *reliantv1.CelDaemonSelector) {
	ds := sel.GetLiteral()
	if ds == nil || ds.GetPool() != "" {
		return
	}
	var pinned string
	switch {
	case ds.GetId() != "":
		pinned = "id " + ds.GetId()
	case ds.GetName() != "":
		pinned = ds.GetName()
	default:
		return
	}
	what := "this workflow"
	if path != "" {
		what = "this step"
	}
	c.add(RuleDaemonMachinePin, path, fmt.Sprintf("daemon: pins %s to one machine (%s): when that box is swapped out or busy, every run stalls waiting for it", what, pinned),
		"pin a machine pool instead: daemon: {pool: <name>}")
}

func (c *checker) checkWorkflow() {
	wf := c.wf
	c.checkMachinePin("", wf.GetDaemon())
	// finally runs on a failure too (run.status == 'failed'), so either
	// handler is a failure path.
	if len(wf.GetTriggers()) > 0 && wf.GetOnFailure() == nil && wf.GetFinally() == nil {
		c.add(RuleNoFailurePath, "", "the workflow is started by triggers and has no on_failure or finally, so an unattended run that fails reports to nobody",
			"add on_failure: (or finally:, which also runs on a cancel) with a step that tells someone (a comment, a message, an issue)")
	}
	if wf.GetAutomationOnly() && len(wf.GetTriggers()) == 0 {
		c.add(RuleAutomationUntriggered, "", "automation_only: true and no triggers: no chat can start this workflow and nothing else will",
			"declare its triggers, or drop automation_only")
	}

	nodes := map[string]*reliantv1.Node{}
	for _, g := range graphsOf(wf) {
		for _, n := range g.wf.GetNodes() {
			if _, dup := nodes[n.GetId()]; !dup {
				nodes[n.GetId()] = n
			}
		}
	}
	for _, d := range c.reads.decodes() {
		n := nodes[d.node]
		if n == nil {
			continue
		}
		switch n.GetType() {
		case model.NodeTypeCallLLM:
			if model.GetCallLLMArgs(n).GetResponseTool() == nil {
				c.add(RuleAgentDecodeFreeText, d.node, fmt.Sprintf("json.decode(nodes.%s.%s) parses a model's free text, which no contract shapes", d.node, d.field),
					"give the node a response_tool with a schema and read its fields")
			}
		case model.NodeTypeWorkflow, model.NodeTypeLoop:
			if childRef(n) == "" {
				continue
			}
			var args map[string]bool
			if swa := model.GetSubWorkflowArgs(n); swa != nil {
				args = keysOf(swa.GetArgs())
			} else {
				args = keysOf(model.GetLoopArgs(n).GetArgs())
			}
			if !args["response_schema"] {
				c.add(RuleAgentDecodeFreeText, d.node, fmt.Sprintf("json.decode(nodes.%s.%s) parses an agent's free text, which no contract shapes", d.node, d.field),
					"pass args.response_schema (and response_tool_name) and read nodes."+d.node+".response.<field>")
			}
		}
	}
}

// secretPatterns are credential shapes that never belong in a definition.
// random says the match must mix digits and letters, as a generated token
// does: it keeps `sk-<long-kebab-name>` and prose from matching.
var secretPatterns = []struct {
	name   string
	re     *regexp.Regexp
	random bool
}{
	{"a GitHub token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})`), true},
	{"an API key", regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{32,}`), true},
	{"a Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{20,}`), true},
	{"an AWS access key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), true},
	{"a private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), false},
	{"a bearer token", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{24,}=*`), true},
}

var secretEnvName = regexp.MustCompile(`(?i)(token|secret|passw(or)?d|api_?key|private_?key|credential)`)

func (c *checker) checkSecrets() {
	scan := func(node string, texts []string) {
		reported := map[string]bool{}
		for _, t := range texts {
			for _, p := range secretPatterns {
				m := p.re.FindString(t)
				if m == "" || reported[p.name] || (p.random && !hasDigitAndLetter(m)) {
					continue
				}
				reported[p.name] = true
				c.add(RuleSecretLiteral, node, fmt.Sprintf("the definition contains %s (%s…)", p.name, m[:min(len(m), 8)]),
					"remove it, revoke it, and read it through a credential: credentials: inherit, an action's connection:, or a hosted secret")
			}
		}
	}
	for _, g := range graphsOf(c.wf) {
		for _, n := range g.wf.GetNodes() {
			path := g.prefix + n.GetId()
			if body := inlineBody(n); body != nil {
				// The body's nodes are scanned as their own graph.
				shallow := proto.Clone(n).(*reliantv1.Node)
				if la := model.GetLoopArgs(shallow); la != nil {
					la.Inline = nil
				} else if swa := model.GetSubWorkflowArgs(shallow); swa != nil {
					swa.Inline = nil
				}
				scan(path, stringsOf(shallow))
			} else {
				scan(path, stringsOf(n))
			}
			if run := model.GetRunArgs(n); run != nil {
				for _, k := range sortedKeys(run.GetEnv()) {
					v := strings.TrimSpace(run.GetEnv()[k])
					if secretEnvName.MatchString(k) && v != "" && !strings.Contains(v, "{{") && !strings.HasPrefix(v, "$") {
						c.add(RuleSecretLiteral, path, fmt.Sprintf("env %s is set to a literal value", k),
							"read it through a credential (credentials: inherit puts the machine's in the environment), never in the file")
					}
				}
			}
		}
	}
	rest := proto.Clone(c.wf).(*reliantv1.Workflow)
	rest.Nodes, rest.OnFailure, rest.Finally = nil, nil, nil
	scan("", stringsOf(rest))
}

func hasDigitAndLetter(s string) bool {
	return strings.ContainsAny(s, "0123456789") && strings.ContainsAny(strings.ToLower(s), "abcdefghijklmnopqrstuvwxyz")
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkAskQuestionType: the app and `reliant workflow answer` read an
// ask_question's metadata as a question card only when it says
// "type": "ask_user"; without it the gate shows no options and nothing can
// answer it, while validate and scenarios pass. Only metadata written as a
// literal JSON object is judged: an expression's value does not exist yet.
func (c *checker) checkAskQuestionType(path string, n *reliantv1.Node) {
	raw := strings.TrimSpace(model.CelStringRaw(n.GetAskQuestion().GetMetadata()))
	if raw == "" {
		return
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return
	}
	if t, _ := meta["type"].(string); t == "ask_user" {
		return
	}
	c.add(RuleAskQuestionType, path, `metadata has no "type": "ask_user", so the app shows the question with no options and `+"`reliant workflow answer`"+` cannot answer it`,
		`add "type": "ask_user" to the metadata JSON, beside "questions"`)
}
