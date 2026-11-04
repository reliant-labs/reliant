package wfyaml

import (
	"fmt"
	"sort"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

// unmarshalWorkflowSecrets reads the top-level `secrets:` block:
//
//	secrets:
//	  TRADER_KEY:
//	    environment: trader/staging   # or "{{ inputs.trader_env }}"
//
// A step then writes {{ secrets.TRADER_KEY }}; the declaration says where that
// secret lives. The placeholder is not CEL: the runtime leaves it literal in
// workflow state and resolves it inside the step's activity.
func unmarshalWorkflowSecrets(node *yaml.Node) (map[string]*reliantv1.SecretDeclaration, error) {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("secrets must be a mapping of secret name to {environment: ...}")
	}
	out := make(map[string]*reliantv1.SecretDeclaration, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		name, val := node.Content[i].Value, node.Content[i+1]
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("secrets.%s is declared twice", name)
		}
		if val.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("secrets.%s must be a mapping with an environment", name)
		}
		decl := &reliantv1.SecretDeclaration{}
		for j := 0; j+1 < len(val.Content); j += 2 {
			switch key := val.Content[j].Value; key {
			case "environment":
				env, err := unmarshalCelString(val.Content[j+1])
				if err != nil {
					return nil, fmt.Errorf("secrets.%s.environment: %w", name, err)
				}
				decl.Environment = env
			default:
				return nil, unknownField(key, "unknown field %q in secrets.%s (environment)", key, name)
			}
		}
		out[name] = decl
	}
	return out, nil
}

func marshalWorkflowSecrets(secrets map[string]*reliantv1.SecretDeclaration) (*yaml.Node, error) {
	if len(secrets) == 0 {
		return nil, nil
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	names := make([]string, 0, len(secrets))
	for name := range secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := &yaml.Node{Kind: yaml.MappingNode}
		if env := secrets[name].GetEnvironment(); env != nil {
			en, err := marshalCelString(env)
			if err != nil {
				return nil, err
			}
			if en != nil {
				entry.Content = append(entry.Content, scalarNode("environment", ""), en)
			}
		}
		m.Content = append(m.Content, scalarNode(name, ""), entry)
	}
	return m, nil
}

// bindNodeSecretRefs records, on every action node that uses a declared secret,
// which secrets its activity must resolve and where each lives. The refs carry
// names and environment expressions only. An action resolves the secrets its
// credential slots name: an http/request@1 header value and credential.secret.
// No other node resolves any (a run step's command could print it).
//
// A reference to an undeclared name gets no ref here; validation reports it.
// Inline loop and workflow bodies share the root's declarations. A workflow
// loaded by ref is parsed on its own and declares its own secrets.
func bindNodeSecretRefs(wf *reliantv1.Workflow) {
	decls := wf.GetSecrets()
	var walk func(*reliantv1.Workflow)
	walk = func(w *reliantv1.Workflow) {
		if w == nil {
			return
		}
		for _, node := range w.GetNodes() {
			node.SecretRefs = nil
			var names []string
			if args := node.GetAction(); args != nil {
				set := map[string]bool{}
				for _, n := range stepsecrets.AuthoredNamesInValue(args.GetWith()["headers"]) {
					set[n] = true
				}
				if c := args.GetCredential(); c != nil && c.GetSecret() != "" {
					set[c.GetSecret()] = true
				}
				for n := range set {
					names = append(names, n)
				}
			}
			sort.Strings(names)
			for _, name := range names {
				d, ok := decls[name]
				if !ok || d.GetEnvironment() == nil {
					continue
				}
				node.SecretRefs = append(node.SecretRefs, &reliantv1.NodeSecretRef{
					Name:        name,
					Environment: proto.Clone(d.GetEnvironment()).(*reliantv1.CelString),
				})
			}
			walk(node.GetWorkflow().GetInline())
			walk(node.GetLoop().GetInline())
		}
		walk(w.GetOnFailure())
	}
	walk(wf)
}
