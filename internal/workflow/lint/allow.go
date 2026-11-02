// Copyright (c) 2025 Reliant Labs
package lint

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// A finding the author has judged is silenced where it is, with the reason:
//
//	# lint:allow run-exit-code-unread — the gate's stdout RESULT= line is the verdict
//	- id: gate
//
// A comment on (or inside) a node's mapping covers that node; one on a
// top-level key (name:, triggers:, …) covers the whole workflow. Several
// rules may be named, comma-separated. The reason is required: an allowance
// is a decision someone has to be able to read later.
var allowPattern = regexp.MustCompile(`lint:allow\s+([a-z0-9-]+(?:\s*,\s*[a-z0-9-]+)*)\s*(?:[—–:-]+\s*)?(.*)$`)

type allowances struct {
	workflow map[string]bool            // rule -> allowed for the whole workflow
	nodes    map[string]map[string]bool // node id -> rule -> allowed
}

func (a allowances) covers(f Finding) bool {
	if a.workflow[f.Rule] {
		return true
	}
	if f.Node == "" {
		return false
	}
	id := f.Node[strings.LastIndex(f.Node, ".")+1:]
	return a.nodes[id][f.Rule]
}

func parseAllowances(src []byte) (allowances, []Finding) {
	a := allowances{workflow: map[string]bool{}, nodes: map[string]map[string]bool{}}
	if len(src) == 0 {
		return a, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil || len(doc.Content) == 0 {
		return a, nil
	}
	var problems []Finding
	record := func(node string, comments ...string) {
		for _, block := range comments {
			for _, line := range strings.Split(block, "\n") {
				m := allowPattern.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				reason := strings.TrimSpace(m[2])
				for _, id := range strings.Split(m[1], ",") {
					id = strings.TrimSpace(id)
					if _, ok := ruleSeverity(id); !ok {
						problems = append(problems, Finding{Rule: RuleAllowInvalid, Severity: SeverityError, Node: node,
							Message: fmt.Sprintf("lint:allow names %q, which is not a rule", id), Fix: "use an ID `reliant workflow lint --rules` lists", Docs: docsFor(RuleAllowInvalid)})
						continue
					}
					if reason == "" {
						problems = append(problems, Finding{Rule: RuleAllowInvalid, Severity: SeverityError, Node: node,
							Message: fmt.Sprintf("lint:allow %s gives no reason", id), Fix: "say why, after the rule: `# lint:allow " + id + " — <why it is fine here>`", Docs: docsFor(RuleAllowInvalid)})
						continue
					}
					if node == "" {
						a.workflow[id] = true
					} else {
						if a.nodes[node] == nil {
							a.nodes[node] = map[string]bool{}
						}
						a.nodes[node][id] = true
					}
				}
			}
		}
	}

	root := doc.Content[0]
	record("", doc.HeadComment, doc.LineComment, root.HeadComment)
	if root.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(root.Content); i += 2 {
			k := root.Content[i]
			record("", k.HeadComment, k.LineComment)
		}
	}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		switch n.Kind {
		case yaml.MappingNode:
			if id := workflowNodeID(n); id != "" {
				comments := []string{n.HeadComment, n.LineComment}
				for _, child := range n.Content {
					comments = append(comments, child.HeadComment, child.LineComment)
				}
				record(id, comments...)
			}
			for _, child := range n.Content {
				walk(child)
			}
		case yaml.SequenceNode, yaml.DocumentNode:
			for _, child := range n.Content {
				walk(child)
			}
		}
	}
	walk(root)
	return a, problems
}

// workflowNodeID is the id of a mapping that is a workflow node (it has both
// id: and type:), "" for any other mapping.
func workflowNodeID(n *yaml.Node) string {
	id, typed := "", false
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch n.Content[i].Value {
		case "id":
			id = n.Content[i+1].Value
		case "type":
			typed = true
		}
	}
	if !typed {
		return ""
	}
	return id
}
