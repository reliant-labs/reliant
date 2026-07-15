package runtime

import (
	"strconv"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// simDefaultCompactionThreshold mirrors the global fallback in the call_llm
// handler (handlers.DefaultCompactionThreshold). The simulator can't resolve
// per-model defaults, so it applies the explicit arg when set and otherwise
// this global default.
const simDefaultCompactionThreshold = 185000

// applyCallLLMCompactionThreshold injects the compaction_threshold field onto a
// call_llm node's mock output, mirroring explicitCompactionThresholdArg in the
// handler: an explicit positive arg wins, otherwise the global default. Mock
// outputs never carry this field, so without it the agent-loop compact edge
// (nodes.call_llm.compaction_threshold) would see 0 and always fire.
func applyCallLLMCompactionThreshold(output map[string]interface{}, node *reliantv1.Node, evaluatedInputs map[string]interface{}) {
	if output == nil || node == nil || node.GetType() != model.NodeTypeCallLLM {
		return
	}
	threshold := simDefaultCompactionThreshold
	if v, ok := simCoerceInt(evaluatedInputs["compaction_threshold"]); ok && v > 0 {
		threshold = v
	}
	output["compaction_threshold"] = threshold
}

// simCoerceInt converts a CEL-evaluated numeric arg (which may be int, int64,
// float64, or a protojson string) to an int.
func simCoerceInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	case string:
		// protojson serializes CelInt int64 literals as JSON strings.
		if parsed, err := strconv.Atoi(n); err == nil {
			return parsed, true
		}
		return 0, false
	default:
		return 0, false
	}
}
