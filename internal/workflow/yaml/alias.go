package wfyaml

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// maxAliasExpansionNodes caps how many nodes alias expansion may copy, so a
// definition built from nested aliases (a "billion laughs" document) is
// refused instead of exhausting memory on the server that parses it. A real
// workflow copies a few dozen.
const maxAliasExpansionNodes = 100_000

// expandYAMLAliases rewrites the tree under root so it holds no alias nodes and
// no merge keys: every alias (`*name`) is replaced by a copy of the node its
// anchor names, and every merge key (`<<: *base`, `<<: [*a, *b]`) by the pairs
// it merges in. Every decoder in this package reads a node's Kind, Value and
// Content directly, and an alias node has none of them, so without this a
// `command: *disarm` was decoded through yaml.v3's generic path as literal
// text and its {{ }} template never expanded. After it, an alias reads exactly
// as the text it points to.
//
// Merge keys follow YAML's merge semantics: a key written in the mapping wins
// over a merged one wherever it appears, and among merged mappings the earlier
// one wins. Merged pairs take the merge key's place, so the author's order
// survives.
func expandYAMLAliases(root *yaml.Node) (*yaml.Node, error) {
	e := &aliasExpander{inProgress: map[*yaml.Node]bool{}}
	out, err := e.expand(root)
	if err != nil {
		return nil, fmt.Errorf("v2yaml: %w", err)
	}
	return out, nil
}

type aliasExpander struct {
	// inProgress holds the anchored nodes being expanded, so an alias to an
	// ancestor of itself is refused rather than recursed into forever.
	inProgress map[*yaml.Node]bool
	// underAlias is how many aliases the current node was reached through;
	// only those copies count toward maxAliasExpansionNodes.
	underAlias int
	copied     int
}

func (e *aliasExpander) expand(n *yaml.Node) (*yaml.Node, error) {
	if n == nil {
		return nil, nil
	}
	if n.Kind == yaml.AliasNode {
		if n.Alias == nil {
			return nil, fmt.Errorf("line %d: alias *%s names no anchor", n.Line, n.Value)
		}
		e.underAlias++
		defer func() { e.underAlias-- }()
		return e.expand(n.Alias)
	}
	if e.inProgress[n] {
		return nil, fmt.Errorf("line %d: anchor &%s contains an alias to itself", n.Line, n.Anchor)
	}
	if e.underAlias > 0 {
		e.copied++
		if e.copied > maxAliasExpansionNodes {
			return nil, fmt.Errorf("line %d: aliases expand to more than %d nodes", n.Line, maxAliasExpansionNodes)
		}
	}

	out := *n
	// The copy holds no aliases, so nothing refers to its anchor any more;
	// clearing it keeps a re-marshaled tree from declaring one anchor twice.
	out.Anchor = ""
	out.Content = nil
	if len(n.Content) == 0 {
		return &out, nil
	}

	e.inProgress[n] = true
	defer delete(e.inProgress, n)

	if n.Kind != yaml.MappingNode {
		out.Content = make([]*yaml.Node, 0, len(n.Content))
		for _, c := range n.Content {
			ec, err := e.expand(c)
			if err != nil {
				return nil, err
			}
			out.Content = append(out.Content, ec)
		}
		return &out, nil
	}
	return e.expandMapping(n, &out)
}

func (e *aliasExpander) expandMapping(n, out *yaml.Node) (*yaml.Node, error) {
	// Keys written in the mapping itself win over merged ones wherever they
	// appear, so collect them first.
	explicit := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := resolveAliasNode(n.Content[i]); !isMergeKey(k) {
			explicit[k.Value] = true
		}
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if isMergeKey(n.Content[i]) {
			pairs, err := e.mergedPairs(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			for j := 0; j+1 < len(pairs); j += 2 {
				key := pairs[j].Value
				if explicit[key] || seen[key] {
					continue
				}
				seen[key] = true
				out.Content = append(out.Content, pairs[j], pairs[j+1])
			}
			continue
		}
		k, err := e.expand(n.Content[i])
		if err != nil {
			return nil, err
		}
		v, err := e.expand(n.Content[i+1])
		if err != nil {
			return nil, err
		}
		seen[k.Value] = true
		out.Content = append(out.Content, k, v)
	}
	return out, nil
}

// mergedPairs is the expanded key/value pairs a merge key's value contributes:
// a mapping's own, or for a list of mappings each one's in turn (the caller
// keeps the first of a repeated key, so the earlier mapping wins).
func (e *aliasExpander) mergedPairs(v *yaml.Node) ([]*yaml.Node, error) {
	ev, err := e.expand(v)
	if err != nil {
		return nil, err
	}
	switch ev.Kind {
	case yaml.MappingNode:
		return ev.Content, nil
	case yaml.SequenceNode:
		var pairs []*yaml.Node
		for _, item := range ev.Content {
			if item.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("line %d: a merge key (<<) list may hold only mappings", item.Line)
			}
			pairs = append(pairs, item.Content...)
		}
		return pairs, nil
	default:
		return nil, fmt.Errorf("line %d: a merge key (<<) takes a mapping or a list of mappings", v.Line)
	}
}

// isMergeKey reports whether k is YAML's merge key, as yaml.v3 decides it: a
// plain `<<` (or one explicitly tagged !!merge), not the quoted string "<<".
func isMergeKey(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "" || k.Tag == "!" || k.ShortTag() == "!!merge")
}

func resolveAliasNode(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}
