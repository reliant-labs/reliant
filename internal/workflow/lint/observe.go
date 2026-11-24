package lint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
)

// checkObserve reports what validation found in the workflow's observe
// blocks (their shape and their expressions) as observe-invalid errors.
// Validation only ever warns about observe, because it runs as a run loads
// its definition and a node's observability must never stop it. Here, where
// a definition is reviewed, a typo in observe is stopped.
func (c *checker) checkObserve() {
	if !usesObserve(c.wf) {
		return
	}
	var loader validation.WorkflowLoader
	if c.in.LoadRef != nil {
		loader = validation.WorkflowLoader(c.in.LoadRef)
	}
	for _, e := range validation.ObserveFindings(validation.StaticAnalysis(c.wf, loader)) {
		node, where := observeFindingLocation(e.Path)
		msg := e.Message
		if where != "" {
			msg = where + ": " + msg
		}
		c.add(RuleObserveInvalid, node, msg, e.Suggestion)
	}
}

// usesObserve reports whether any graph of wf declares an observe block.
func usesObserve(wf *reliantv1.Workflow) bool {
	for _, g := range graphsOf(wf) {
		if g.wf.GetObserve() != nil {
			return true
		}
		for _, n := range g.wf.GetNodes() {
			if n.GetObserve() != nil {
				return true
			}
		}
	}
	return false
}

// indexedNodeSegment is how part of validation names a node in a path,
// "[2](plan)"; the rest names it by its id alone.
var indexedNodeSegment = regexp.MustCompile(`^\[\d+\]\((.*)\)$`)

// observeFindingLocation turns a validation path into lint's node path
// ("plan", "loop.body", "on_failure.notify"; "" for the workflow's own
// defaults) and the place inside the observe block ("events.[0].when").
func observeFindingLocation(path []string) (node, where string) {
	var nodes []string
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case "on_failure", "finally":
			if len(nodes) == 0 {
				nodes = append(nodes, path[i])
			}
		case "nodes":
			if i+1 < len(path) {
				seg := path[i+1]
				if m := indexedNodeSegment.FindStringSubmatch(seg); m != nil {
					seg = m[1]
				}
				nodes = append(nodes, seg)
				i++
			}
		case "observe":
			where = strings.Join(path[i+1:], ".")
			if len(nodes) == 1 && (nodes[0] == "on_failure" || nodes[0] == "finally") {
				// A handler's own defaults: reported on the workflow.
				return "", strings.TrimSuffix(nodes[0]+".observe."+where, ".")
			}
			return strings.Join(nodes, "."), where
		}
	}
	return strings.Join(nodes, "."), ""
}

// decisionWords are the words that name a decision in an output or node id
// (dev-docs/design/workflow-observe.md, section 10). A name matches when one
// of its _- separated words is one of these.
var decisionWords = map[string]bool{
	"decision": true, "decide": true, "verdict": true, "reason": true, "why": true,
	"skip": true, "cut": true, "hold": true, "land": true, "take": true,
	"route": true, "tier": true, "label": true, "handoff": true, "unstick": true,
}

// namesADecision reports whether name reads as a decision: one of its words
// is in decisionWords, or it is give_up.
func namesADecision(name string) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "give_up") || strings.Contains(lower, "give-up") {
		return true
	}
	for _, w := range strings.FieldsFunc(lower, func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		if decisionWords[w] {
			return true
		}
	}
	return false
}

// checkObserveDecisions reports a node that looks like it decides something
// another node acts on and records nothing about it: a compute node with a
// decision-named output something reads, or a run node with a decision-named
// id whose stdout or exit code something reads. A node with an observe:
// block of its own, or in a graph whose defaults record an event, is
// covered. It is a heuristic and a warning; `lint:allow` silences it.
func (c *checker) checkObserveDecisions() {
	for _, g := range graphsOf(c.wf) {
		defaultsRecord := len(g.wf.GetObserve().GetEvents()) > 0
		if defaultsRecord {
			continue
		}
		for _, n := range g.wf.GetNodes() {
			if n.GetObserve() != nil {
				continue
			}
			id, path := n.GetId(), g.prefix+n.GetId()
			var read []string
			switch n.GetType() {
			case model.NodeTypeCompute:
				names := make([]string, 0, len(n.GetCompute().GetOutputs()))
				for name := range n.GetCompute().GetOutputs() {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					if namesADecision(name) && c.reads.reads(id, name) {
						read = append(read, name)
					}
				}
			case model.NodeTypeRun:
				if namesADecision(id) && (c.reads.reads(id, "stdout") || c.reads.reads(id, "exit_code")) {
					read = append(read, "stdout")
				}
			}
			if len(read) == 0 {
				continue
			}
			c.add(RuleObserveDecision, path,
				fmt.Sprintf("this node decides something other nodes act on (nodes.%s.%s) and records no decision, so the run view, `reliant workflow status` and traces cannot say what it chose or why", id, read[0]),
				"add observe: with an event named decision (name, value, reason), or allow it with the reason the choice needs no record")
		}
	}
}
