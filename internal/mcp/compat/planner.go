package compat

import "github.com/reliant-labs/reliant/internal/mcp/protocol"

// BuildAttemptPlan creates the deterministic attempt sequence for a tool call.
func BuildAttemptPlan(arguments map[string]interface{}, preferred EnvelopeName) []Attempt {
	if protocol.HasExplicitEnvelopeShape(arguments) {
		return []Attempt{{
			Name:    EnvelopeDirect,
			Payload: protocol.BuildDirectPayload(arguments),
		}}
	}

	basePayloads := protocol.BuildCompatibilityEnvelopes(arguments)
	base := []Attempt{
		{Name: EnvelopeParams, Payload: basePayloads[0]},
		{Name: EnvelopeParamsApplicationJSON, Payload: basePayloads[1]},
		{Name: EnvelopeParamsArguments, Payload: basePayloads[2]},
		{Name: EnvelopeParamsArgumentsApplication, Payload: basePayloads[3]},
	}

	lookup := make(map[EnvelopeName]Attempt, len(base)+1)
	lookup[EnvelopeDirect] = Attempt{Name: EnvelopeDirect, Payload: protocol.BuildDirectPayload(arguments)}
	for _, a := range base {
		lookup[a.Name] = a
	}

	ordered := make([]Attempt, 0, len(lookup))
	seen := make(map[EnvelopeName]struct{}, len(lookup))

	push := func(name EnvelopeName) {
		a, ok := lookup[name]
		if !ok {
			return
		}
		if _, exists := seen[name]; exists {
			return
		}
		if ShouldSkipEnvelopeForArguments(name, arguments) {
			return
		}
		ordered = append(ordered, a)
		seen[name] = struct{}{}
	}

	push(EnvelopeDirect)
	if preferred != "" {
		push(preferred)
	}
	for _, a := range base {
		push(a.Name)
	}

	return ordered
}

// ShouldSkipEnvelopeForArguments avoids re-wrapping already wrapped payloads.
func ShouldSkipEnvelopeForArguments(envelope EnvelopeName, arguments map[string]interface{}) bool {
	if arguments == nil {
		return false
	}

	switch envelope {
	case EnvelopeParams:
		if _, has := arguments["params"]; has {
			return true
		}
	case EnvelopeParamsApplicationJSON:
		if params, ok := arguments["params"].(map[string]interface{}); ok {
			if _, has := params["application/json"]; has {
				return true
			}
		}
	case EnvelopeParamsArguments:
		if params, ok := arguments["params"].(map[string]interface{}); ok {
			if _, has := params["arguments"]; has {
				return true
			}
		}
	case EnvelopeParamsArgumentsApplication:
		if params, ok := arguments["params"].(map[string]interface{}); ok {
			if argsObj, ok := params["arguments"].(map[string]interface{}); ok {
				if _, has := argsObj["application/json"]; has {
					return true
				}
			}
		}
	}

	return false
}
