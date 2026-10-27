package wfyaml

import (
	"fmt"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"gopkg.in/yaml.v3"
)

func unmarshalConcurrency(n *yaml.Node) (*reliantv1.WorkflowConcurrency, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("concurrency must be a mapping")
	}
	c := &reliantv1.WorkflowConcurrency{}
	seen := map[string]bool{}
	for i := 0; i < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		if seen[k] {
			return nil, fmt.Errorf("duplicate concurrency field %q", k)
		}
		seen[k] = true
		var err error
		switch k {
		case "max_running":
			err = v.Decode(&c.MaxRunning)
		case "key":
			err = v.Decode(&c.Key)
		case "supersede":
			err = v.Decode(&c.Supersede)
		default:
			return nil, fmt.Errorf("unknown concurrency field %q", k)
		}
		if err != nil {
			return nil, fmt.Errorf("concurrency.%s: %w", k, err)
		}
	}
	return c, nil
}
