// Copyright (c) 2025 Reliant Labs
package runtime

import "go.temporal.io/sdk/workflow"

// scenarioStateKey is deliberately private: fixtures are in-memory harness
// context, never workflow inputs, history payloads, or recovery state.
type scenarioStateKey struct{}

// WithScenarioState supplies node-output fixtures to the scenario harness's root
// DynamicWorkflow invocation. Production callers must not use it to restore
// outputs on resume. Each invocation gets its own copy of the supplied fixtures.
func WithScenarioState(ctx workflow.Context, state map[string]map[string]interface{}) workflow.Context {
	if len(state) == 0 {
		return ctx
	}
	outputs := make(map[string]interface{}, len(state))
	for id, output := range state {
		outputs[id] = deepCopyJSONMap(output)
	}
	return workflow.WithValue(ctx, scenarioStateKey{}, outputs)
}

// takeScenarioState shadows the seed before any nested execution can inherit
// it. The ordinary production path returns the original, unmodified context.
func takeScenarioState(ctx workflow.Context) (map[string]interface{}, workflow.Context) {
	outputs, _ := ctx.Value(scenarioStateKey{}).(map[string]interface{})
	if outputs == nil {
		return nil, ctx
	}
	return outputs, workflow.WithValue(ctx, scenarioStateKey{}, map[string]interface{}(nil))
}
