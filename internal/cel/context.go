// Copyright (c) 2025 Reliant Labs
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package cel

// EvaluationContext holds variables for CEL evaluation
type EvaluationContext struct {
	vars map[string]interface{}
}

// NewEvaluationContext creates a new context
func NewEvaluationContext() *EvaluationContext {
	return &EvaluationContext{
		vars: make(map[string]interface{}),
	}
}

// Set adds a variable to the context
func (c *EvaluationContext) Set(key string, value interface{}) {
	c.vars[key] = value
}

// SetAll merges variables into the context
func (c *EvaluationContext) SetAll(vars map[string]interface{}) {
	for k, v := range vars {
		c.vars[k] = v
	}
}

// Get retrieves a variable from the context
func (c *EvaluationContext) Get(key string) (interface{}, bool) {
	val, ok := c.vars[key]
	return val, ok
}

// Variables returns all variables
func (c *EvaluationContext) Variables() map[string]interface{} {
	return c.vars
}

// Clone creates a copy of the context
func (c *EvaluationContext) Clone() *EvaluationContext {
	clone := NewEvaluationContext()
	for k, v := range c.vars {
		clone.vars[k] = v
	}
	return clone
}

// Common variable builders for workflow use

// BuildToolContext creates context for tool-related conditions
func BuildToolContext(toolName string, toolInput map[string]interface{}, autoApprove bool) *EvaluationContext {
	ctx := NewEvaluationContext()
	ctx.Set("tool", map[string]interface{}{
		"name":  toolName,
		"input": toolInput,
	})
	ctx.Set("context", map[string]interface{}{
		"auto_approve": autoApprove,
	})
	return ctx
}

// BuildMessageContext creates context for message-related conditions
func BuildMessageContext(role, content string, metadata map[string]interface{}) *EvaluationContext {
	ctx := NewEvaluationContext()
	ctx.Set("message", map[string]interface{}{
		"role":    role,
		"content": content,
	})
	ctx.SetAll(metadata)
	return ctx
}

// BuildWorkflowContext creates context for workflow-level conditions
func BuildWorkflowContext(workflowID, chatID string, state map[string]interface{}) *EvaluationContext {
	ctx := NewEvaluationContext()
	ctx.Set("workflow", map[string]interface{}{
		"id": workflowID,
	})
	ctx.Set("chat", map[string]interface{}{
		"id": chatID,
	})
	ctx.SetAll(state)
	return ctx
}
