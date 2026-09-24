package runtime

import (
	"strconv"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// mockedDefaultCompactionThreshold mirrors the global FALLBACK in the call_llm
// handler (handlers.DefaultCompactionThreshold / models.UnknownModelCompactionFloor).
// At runtime, when no explicit arg is set, the handler DERIVES the threshold from
// the resolved model's real context window; a scenario's mocked CallLLM does not
// resolve per-model windows, so it applies the explicit arg when set and otherwise
// this shared global default.
const mockedDefaultCompactionThreshold = models.UnknownModelCompactionFloor

// ApplyMockedCompactionThreshold injects compaction_threshold onto a mocked
// call_llm output, reading the explicit arg off the node's EVALUATED args.
//
// Exported for the scenario runner, which mocks the CallLLM activity: the real
// activity always returns a non-zero threshold (explicit arg, else a
// model-derived value, else DefaultCompactionThreshold), so a mock that omits
// the field makes the agent-loop compact edge
// (nodes.execute_tools.thread_token_count > nodes.call_llm.compaction_threshold)
// compare against 0 and fire on every iteration.
func ApplyMockedCompactionThreshold(output map[string]interface{}, node *reliantv1.Node) {
	if output == nil || node == nil || node.GetType() != model.NodeTypeCallLLM {
		return
	}
	threshold := mockedDefaultCompactionThreshold
	if args := model.GetCallLLMArgs(node); args != nil {
		if v, ok := coerceMockedInt(model.CelIntValue(args.GetCompactionThreshold())); ok && v > 0 {
			threshold = v
		}
	}
	output["compaction_threshold"] = threshold
}

// coerceMockedInt converts a CEL-evaluated numeric arg (which may be int, int64,
// float64, or a protojson string) to an int.
func coerceMockedInt(v interface{}) (int, bool) {
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
