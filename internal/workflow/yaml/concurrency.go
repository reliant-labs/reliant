package wfyaml

import (
	"fmt"
	"strconv"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"gopkg.in/yaml.v3"
)

// concurrencyWaiting maps the YAML spelling of concurrency.waiting.
var concurrencyWaiting = map[string]reliantv1.WorkflowConcurrencyWaiting{
	"fifo":   reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_FIFO,
	"latest": reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST,
}

// concurrencyCancelRunning maps the YAML spelling of concurrency.cancel_running.
var concurrencyCancelRunning = map[string]reliantv1.WorkflowConcurrencyCancelRunning{
	"never":            reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_NEVER,
	"on_new_key_event": reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_KEY_EVENT,
	"on_new_head":      reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_HEAD,
}

func unmarshalWorkflowConcurrency(node *yaml.Node) (*reliantv1.WorkflowConcurrency, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("concurrency must be a mapping with max_running, per_member, max_running_per_key, or a combination")
	}
	policy := &reliantv1.WorkflowConcurrency{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		name, value := node.Content[i].Value, node.Content[i+1]
		if seen[name] {
			return nil, fmt.Errorf("concurrency.%s is set twice", name)
		}
		seen[name] = true
		switch name {
		case "max_running", "per_member", "max_running_per_key":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
				return nil, fmt.Errorf("concurrency.%s must be a positive integer", name)
			}
			n, err := strconv.ParseUint(value.Value, 10, 32)
			if err != nil || n == 0 {
				return nil, fmt.Errorf("concurrency.%s must be between 1 and 4294967295", name)
			}
			switch name {
			case "max_running":
				policy.MaxRunning = uint32(n)
			case "per_member":
				// K runs per member of the trigger's machine pool that can take
				// work; max_running, when set too, caps the scaled value.
				policy.PerMember = uint32(n)
			default:
				// N running runs per rendered key, beside the workflow-wide cap.
				policy.MaxRunningPerKey = uint32(n)
			}
		case "waiting":
			waiting, ok := concurrencyWaiting[value.Value]
			if value.Kind != yaml.ScalarNode || !ok {
				return nil, fmt.Errorf("concurrency.waiting must be fifo or latest, not %q", value.Value)
			}
			policy.Waiting = waiting
		case "key":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
				return nil, fmt.Errorf("concurrency.key must be a non-empty template string")
			}
			policy.Key = value.Value
		case "priority":
			// A CEL expression over `trigger`; validation compiles it.
			if value.Kind != yaml.ScalarNode || value.Value == "" {
				return nil, fmt.Errorf("concurrency.priority must be a non-empty CEL expression over trigger, e.g. \"'main-red' in trigger.payload.labels ? 10 : 0\"")
			}
			policy.Priority = value.Value
		case "cancel_running":
			cancel, ok := concurrencyCancelRunning[value.Value]
			if value.Kind != yaml.ScalarNode || !ok {
				return nil, fmt.Errorf("concurrency.cancel_running must be never, on_new_key_event or on_new_head, not %q", value.Value)
			}
			policy.CancelRunning = cancel
		default:
			return nil, fmt.Errorf("unknown concurrency field %q (max_running, per_member, max_running_per_key, waiting, key, priority, cancel_running)", name)
		}
	}
	if !seen["max_running"] && !seen["per_member"] && !seen["max_running_per_key"] {
		return nil, fmt.Errorf("concurrency requires max_running, per_member, max_running_per_key, or a combination")
	}
	if policy.MaxRunningPerKey > 0 && policy.Key == "" {
		return nil, fmt.Errorf("concurrency.max_running_per_key needs key (the group whose running runs it caps), e.g. key: '{{ trigger.payload.pull_request.number }}'")
	}
	if policy.Key != "" && policy.Waiting != reliantv1.WorkflowConcurrencyWaiting_WORKFLOW_CONCURRENCY_WAITING_LATEST && policy.MaxRunningPerKey == 0 {
		return nil, fmt.Errorf("concurrency.key needs waiting: latest (it groups the waiting firings that replace each other) or max_running_per_key (it groups the running runs that are capped)")
	}
	if cancelsRunning(policy) && (policy.Key == "" || policy.MaxRunningPerKey != 1) {
		return nil, fmt.Errorf("concurrency.cancel_running needs key and max_running_per_key: 1: it cancels the one run holding a key's slot, " +
			"so key must name the group (e.g. key: '{{ trigger.payload.data.pull_request.number }}') and the slot must hold exactly one run")
	}
	return policy, nil
}

// cancelsRunning reports whether concurrency.cancel_running lets a newer
// firing cancel a running run (anything but unset or never).
func cancelsRunning(policy *reliantv1.WorkflowConcurrency) bool {
	switch policy.GetCancelRunning() {
	case reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_KEY_EVENT,
		reliantv1.WorkflowConcurrencyCancelRunning_WORKFLOW_CONCURRENCY_CANCEL_RUNNING_ON_NEW_HEAD:
		return true
	}
	return false
}

// marshalWorkflowConcurrency is the inverse of unmarshalWorkflowConcurrency.
// waiting is emitted only when it was set.
func marshalWorkflowConcurrency(policy *reliantv1.WorkflowConcurrency) (*yaml.Node, error) {
	if policy.MaxRunning == 0 && policy.PerMember == 0 && policy.MaxRunningPerKey == 0 {
		return nil, fmt.Errorf("concurrency requires max_running, per_member, max_running_per_key, or a combination")
	}
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if policy.MaxRunning > 0 {
		node.Content = append(node.Content, scalarNode("max_running", ""), scalarNode(strconv.FormatUint(uint64(policy.MaxRunning), 10), "!!int"))
	}
	if policy.PerMember > 0 {
		node.Content = append(node.Content, scalarNode("per_member", ""), scalarNode(strconv.FormatUint(uint64(policy.PerMember), 10), "!!int"))
	}
	if policy.MaxRunningPerKey > 0 {
		node.Content = append(node.Content, scalarNode("max_running_per_key", ""), scalarNode(strconv.FormatUint(uint64(policy.MaxRunningPerKey), 10), "!!int"))
	}
	for spelling, waiting := range concurrencyWaiting {
		if policy.Waiting == waiting {
			node.Content = append(node.Content, scalarNode("waiting", ""), scalarNode(spelling, ""))
		}
	}
	if policy.Key != "" {
		node.Content = append(node.Content, scalarNode("key", ""), scalarNode(policy.Key, "!!str"))
	}
	if policy.Priority != "" {
		node.Content = append(node.Content, scalarNode("priority", ""), scalarNode(policy.Priority, "!!str"))
	}
	for spelling, cancel := range concurrencyCancelRunning {
		if policy.CancelRunning == cancel {
			node.Content = append(node.Content, scalarNode("cancel_running", ""), scalarNode(spelling, ""))
		}
	}
	return node, nil
}
