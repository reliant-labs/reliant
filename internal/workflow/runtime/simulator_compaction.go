package runtime

import (
	"strconv"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// simDefaultCompactionThreshold mirrors the global FALLBACK in the call_llm
// handler (handlers.DefaultCompactionThreshold / models.UnknownModelCompactionFloor).
// At runtime, when no explicit arg is set, the handler DERIVES the threshold from
// the resolved model's real context window; the simulator works from mock outputs
// and does not resolve per-model windows, so it applies the explicit arg when set
// and otherwise this shared global default.
const simDefaultCompactionThreshold = models.UnknownModelCompactionFloor

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

// ApplyMockedCompactionThreshold injects compaction_threshold onto a mocked
// call_llm output, reading the explicit arg off the node's EVALUATED args.
//
// Exported for the Temporal scenario backend, which mocks the CallLLM activity
// and therefore faces the identical problem the simulator solves here: the real
// activity always returns a non-zero threshold (explicit arg, else a
// model-derived value, else DefaultCompactionThreshold), so a mock that omits
// the field makes the agent-loop compact edge
// (nodes.execute_tools.thread_token_count > nodes.call_llm.compaction_threshold)
// compare against 0 and fire on every iteration.
//
// Both backends share this so the mocked threshold cannot drift between them.
func ApplyMockedCompactionThreshold(output map[string]interface{}, node *reliantv1.Node) {
	if output == nil || node == nil || node.GetType() != model.NodeTypeCallLLM {
		return
	}
	threshold := simDefaultCompactionThreshold
	if args := model.GetCallLLMArgs(node); args != nil {
		if v, ok := simCoerceInt(model.CelIntValue(args.GetCompactionThreshold())); ok && v > 0 {
			threshold = v
		}
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
