package runtime

import (
	"fmt"
	"strings"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// ResolvePresetName resolves a preset name that may contain a CEL template.
//
// Behavior:
//   - If raw does not contain "{{", it is returned as-is (literal preset name).
//   - If raw contains a template, it is evaluated against evalCtx. A pure
//     expression producing a non-string value yields an error. Embedded
//     templates (mixed strings) are interpolated to a string.
//   - If evaluation fails, an error is returned; callers are expected to treat
//     this as a skip (log + continue) rather than a fatal error.
//
// An empty string result (either literal or after evaluation) is returned as
// empty — callers decide how to handle empties (typically: skip).
func ResolvePresetName(raw string, evalCtx wfcel.CELEvalContext) (string, error) {
	if !strings.Contains(raw, "{{") {
		return raw, nil
	}

	value, err := wfcel.EvaluateTemplate(raw, evalCtx)
	if err != nil {
		return "", fmt.Errorf("failed to evaluate preset name template %q: %w", raw, err)
	}

	if value == nil {
		return "", nil
	}

	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("preset name expression %q must evaluate to a string, got %T", raw, value)
	}
	return str, nil
}
