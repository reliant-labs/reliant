package lint

import (
	"regexp"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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
		case "observe_defaults":
			return strings.Join(nodes, "."), strings.Join(path[i:], ".")
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
