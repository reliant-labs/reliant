// Copyright (c) 2025 Reliant Labs
package lint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/proto"
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
			if !g.onFailure && !trivialCommand(cmd) && !c.reads.reads(id, "exit_code") {
				c.add(RuleRunExitCodeUnread, path, "nothing reads this node's exit_code, so a failed command routes like a successful one",
					fmt.Sprintf("branch on nodes.%s.exit_code (an edge case, or a condition downstream)", id))
			}
			c.checkFetch(path, n, cmd)
		case model.NodeTypeAction:
			if !g.onFailure && !c.reads.reads(id, "is_error") {
				c.add(RuleActionErrorUnread, path, "nothing reads this node's is_error, so a failed call routes like a successful one",
					fmt.Sprintf("branch on nodes.%s.is_error, or allow it with the reason the failure does not matter", id))
			}
		case model.NodeTypeWorkflow:
			if slotBound && !timed {
				c.add(RuleSlotUnboundedWait, path, "sub-workflow node has no timeout; a stuck child holds this workflow's max_running slot",
					"set timeout: on the node")
			}
			c.checkTurnBudget(path, n)
		case model.NodeTypeApproval, model.NodeTypeAskQuestion:
			bounded := timed || model.CelStringIsSet(model.GetApprovalArgs(n).GetTimeout())
			if slotBound && !bounded {
				c.add(RuleSlotUnboundedWait, path, n.GetType()+" waits for a person with no timeout, holding this workflow's max_running slot until someone answers",
					"set a timeout, or move the human gate out of the slot-bounded workflow")
			}
		case model.NodeTypeLoop:
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
		if len(e.GetCases()) == 0 || len(e.GetDefault()) > 0 {
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

// trivialCommand is a command whose failure means nothing: a fixed sleep (a
// backoff) or a no-op.
var trivialCommandPattern = regexp.MustCompile(`^\s*(sleep\s+[0-9.]+[smhd]?|true|:)\s*$`)

func trivialCommand(cmd string) bool { return trivialCommandPattern.MatchString(cmd) }

// checkTurnBudget: an agent child (one that declares max_turns) whose node
// leaves it unset runs on the child's default.
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
	def := ""
	if d := in.GetIntegerInput().GetDefault(); d != 0 {
		def = fmt.Sprintf(" (%d)", d)
	}
	c.add(RuleAgentTurnBudget, path, fmt.Sprintf("the agent runs on %s's default max_turns%s; an agent that runs out of turns submits nothing", ref, def),
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

func joinContinuations(s string) string { return strings.ReplaceAll(s, "\\\n", " ") }

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

func (c *checker) checkWorkflow() {
	wf := c.wf
	if len(wf.GetTriggers()) > 0 && wf.GetOnFailure() == nil {
		c.add(RuleNoFailurePath, "", "the workflow is started by triggers and has no on_failure, so an unattended run that fails reports to nobody",
			"add on_failure: with a step that tells someone (a comment, a message, an issue)")
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
	rest.Nodes, rest.OnFailure = nil, nil
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
