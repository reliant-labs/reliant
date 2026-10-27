package reviewpublisheradapter

import (
	"bytes"
	"io"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

// validateReviewOutputExpressions permits only completion-time expressions in
// top-level output string values. It is NOT a general runtime-CEL classifier.
// Everything else must be static, even fields the general runtime can defer.
// HasTemplates retains its loader meaning (any delimiter in the source); true
// is safe only after this structural proof. A false negative is refused too.
func validateReviewOutputExpressions(raw, graph []byte, hasTemplates bool) error {
	denied := reviewpublisher.ErrUnauthorized
	if len(raw) == 0 || len(raw) > 1<<20 || len(graph) == 0 || len(graph) > 1<<20 || (!hasTemplates && bytes.Contains(raw, []byte("{{"))) {
		return denied
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if decoder.Decode(&doc) != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return denied
	}
	var extra yaml.Node
	if decoder.Decode(&extra) != io.EOF {
		return denied
	}
	budget := 20000
	if !reviewStaticYAML(doc.Content[0], 0, &budget, false, true) {
		return denied
	}
	parsed, err := wfyaml.ParseWorkflow(raw)
	if err != nil {
		return denied
	}
	// Strict protobuf JSON rejects unknown fields, duplicate fields and aliases
	// for the same protobuf field. Do not use the runtime's legacy JSON fallback
	// or generic float64 canonicalization as a graph-correspondence proof.
	var recorded reliantv1.Workflow
	if protojson.Unmarshal(graph, &recorded) != nil || !proto.Equal(parsed, &recorded) {
		return denied
	}
	return nil
}

// reviewStaticYAML rejects aliases, anchors, merges, duplicate/non-string keys,
// custom tags and excessive nesting before the workflow parser sees the YAML.
// Only the immediate values in the root outputs mapping may contain templates.
func reviewStaticYAML(n *yaml.Node, depth int, budget *int, outputValue, root bool) bool {
	*budget--
	if n == nil || depth > 64 || *budget < 0 || n.Anchor != "" || n.Kind == yaml.AliasNode {
		return false
	}
	switch n.Kind {
	case yaml.ScalarNode:
		if outputValue {
			return n.Tag == "!!str"
		}
		switch n.Tag {
		case "!!str", "!!null", "!!bool", "!!int", "!!float":
			return !strings.Contains(n.Value, "{{")
		default:
			return false
		}
	case yaml.SequenceNode:
		if outputValue || n.Tag != "!!seq" {
			return false
		}
		for _, child := range n.Content {
			if !reviewStaticYAML(child, depth+1, budget, false, false) {
				return false
			}
		}
		return true
	case yaml.MappingNode:
		if outputValue || n.Tag != "!!map" || len(n.Content)%2 != 0 {
			return false
		}
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			// The parser accepts some snake_case/camelCase aliases. Refuse
			// both spellings together: re-marshaling a map must not change
			// which spelling wins. Conservatively apply this at every scope.
			identity := strings.ToLower(strings.ReplaceAll(key.Value, "_", ""))
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Anchor != "" || strings.Contains(key.Value, "{{") || seen[identity] {
				return false
			}
			seen[identity] = true
			if root && key.Value == "outputs" {
				if !reviewOutputStrings(value, depth+1, budget) {
					return false
				}
			} else if !reviewStaticYAML(value, depth+1, budget, false, false) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func reviewOutputStrings(n *yaml.Node, depth int, budget *int) bool {
	if n.Kind != yaml.MappingNode || n.Tag != "!!map" || n.Anchor != "" || len(n.Content)%2 != 0 || depth > 64 {
		return false
	}
	*budget--
	seen := map[string]bool{}
	for i := 0; i < len(n.Content); i += 2 {
		key := n.Content[i]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Anchor != "" || strings.Contains(key.Value, "{{") || seen[key.Value] {
			return false
		}
		seen[key.Value] = true
		if !reviewStaticYAML(n.Content[i+1], depth+1, budget, true, false) {
			return false
		}
	}
	return *budget >= 0
}
