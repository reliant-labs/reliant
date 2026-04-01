package protocol

// NormalizeArguments ensures nil arguments are represented as an empty object.
func NormalizeArguments(arguments map[string]interface{}) map[string]interface{} {
	if arguments == nil {
		return map[string]interface{}{}
	}
	return arguments
}

// HasExplicitEnvelopeShape returns true when caller already provided wrapped payload shape.
func HasExplicitEnvelopeShape(arguments map[string]interface{}) bool {
	if arguments == nil {
		return false
	}
	if _, ok := arguments["params"]; ok {
		return true
	}
	if _, ok := arguments["arguments"]; ok {
		return true
	}
	if _, ok := arguments["application/json"]; ok {
		return true
	}
	return false
}

// BuildCompatibilityEnvelopes returns all non-direct compatibility wrappers in deterministic order.
func BuildCompatibilityEnvelopes(arguments map[string]interface{}) []map[string]interface{} {
	argObject := map[string]interface{}{}
	if arguments != nil {
		argObject = arguments
	}

	return []map[string]interface{}{
		{
			"params": argObject,
		},
		{
			"params": map[string]interface{}{
				"application/json": argObject,
			},
		},
		{
			"params": map[string]interface{}{
				"arguments": argObject,
			},
		},
		{
			"params": map[string]interface{}{
				"arguments": map[string]interface{}{
					"application/json": argObject,
				},
			},
		},
	}
}

// BuildDirectPayload creates the canonical unwrapped call payload.
func BuildDirectPayload(arguments map[string]interface{}) map[string]interface{} {
	return NormalizeArguments(arguments)
}
