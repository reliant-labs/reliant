# Model Pre-Validation Implementation Plan

## Problem Statement

The error "model is required - must be provided via workflow inputs" occurs at runtime deep inside a workflow (after 3000+ activity events), causing the workflow to pause and require manual intervention. This is a poor user experience.

**Root Cause**: Model validation happens too late - in `call_llm.go:214-215` at activity execution time, not before the workflow starts.

**Goal**: Validate all model references can be resolved BEFORE `ExecuteWorkflow` is called, with clear error messages.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                     ModelSelector                                │
│  ValidateAvailability(ctx *RuntimeValidationContext) error      │
│  - Local model? → check LocalBaseURL is configured              │
│  - Cloud model? → registry.Resolve() with available providers   │
└─────────────────────────────────────────────────────────────────┘
                              ▲
                              │ calls
┌─────────────────────────────┴───────────────────────────────────┐
│                   RuntimeValidator interface                     │
│  ValidateWithContext(value, ctx) error                          │
│  - ModelInput implements this                                   │
│  - Extensible to ToolsInput, PresetInput, etc. in future        │
└─────────────────────────────────────────────────────────────────┘
                              ▲
                              │ implements
┌─────────────────────────────┴───────────────────────────────────┐
│              ScanWorkflowModels (reflection-based)               │
│  - Finds all *models.ModelSelector fields on any node type      │
│  - No hardcoded node types - automatically future-proof         │
│  - Handles nested inline workflows                              │
└─────────────────────────────────────────────────────────────────┘
                              ▲
                              │ uses
┌─────────────────────────────┴───────────────────────────────────┐
│              chat.go: validateModelsAvailable()                  │
│  - Called before ExecuteWorkflow in CreateChat & SendMessage    │
│  - Evaluates CEL expressions like {{inputs.model}}              │
│  - Returns clear errors: "Node 'agent' model cannot resolve..." │
└─────────────────────────────────────────────────────────────────┘
```

## Parallelization Strategy

Tasks can be worked on in parallel by multiple agents:

```
Stream A (models package):        Stream B (workflow/v3):           Stream C (grpc/services):
─────────────────────────        ────────────────────────          ─────────────────────────
Task 1: ValidateAvailability     Task 2: RuntimeValidator          (waits for A & B)
        ↓                                interface                          ↓
        ↓                                ↓                          Task 4: Wire up in
        ↓                        Task 3: Model scanner                      chat.go
        ↓                                ↓                                  ↓
        └────────────────────────────────┴──────────────────────────────────┘
                                         ↓
                              Task 5: Improve call_llm error
                                         ↓
                              Task 6: Tests (after all above)
```

**Parallel Streams:**
- **Stream A** (Task 1): Can be done independently - just needs models package
- **Stream B** (Tasks 2-3): Can be done independently - just needs workflow/v3 package
- **Stream C** (Task 4): Depends on A and B being complete
- **Task 5**: Can be done in parallel with everything (independent improvement)
- **Task 6**: Tests - should be done last or incrementally with each task

---

## Task 1: Add ValidateAvailability to ModelSelector

**File**: `internal/llm/models/types.go`

**Dependencies**: None (can start immediately)

**Implementation**:

```go
// Add to internal/llm/models/types.go

// RuntimeValidationContext provides runtime context for model validation.
// This allows validation to check if a model can actually be resolved
// with the user's configured API keys and providers.
type RuntimeValidationContext struct {
    // AvailableProviders lists provider IDs the user has configured (e.g., "anthropic", "openai")
    AvailableProviders []string
    
    // LocalBaseURL is the configured local model endpoint, empty if not configured
    LocalBaseURL string
}

// ValidateAvailability checks if this ModelSelector can be resolved with the given context.
// For local models (provider="local" or tags contain "local"), it only checks that
// LocalBaseURL is configured. For cloud models, it attempts full resolution.
//
// Returns nil if the model can be resolved, or an error with actionable guidance.
func (m *ModelSelector) ValidateAvailability(ctx *RuntimeValidationContext) error {
    if m == nil {
        return fmt.Errorf("model selector is nil")
    }
    
    if m.ID == "" && len(m.Tags) == 0 {
        return fmt.Errorf("model selector must have 'id' or 'tags' set")
    }
    
    // Check if this is a local model
    isLocal := m.Provider == "local"
    if !isLocal {
        for _, tag := range m.Tags {
            if tag == "local" {
                isLocal = true
                break
            }
        }
    }
    
    // Local models: just verify endpoint is configured
    if isLocal {
        if ctx.LocalBaseURL == "" {
            return fmt.Errorf("model requires local provider but no local endpoint configured in config.yaml; add models.providers.local.base_url")
        }
        return nil
    }
    
    // Cloud models: attempt full resolution
    if len(ctx.AvailableProviders) == 0 {
        return fmt.Errorf("no API keys configured; add an API key in Settings for Anthropic, OpenAI, Google, or xAI")
    }
    
    registry := MustGetRegistry()
    _, err := registry.Resolve(*m, ctx.AvailableProviders)
    if err != nil {
        // Provide actionable error message
        return fmt.Errorf("cannot resolve model %s: %w; check your API key configuration in Settings", m.String(), err)
    }
    
    return nil
}

// String returns a human-readable representation of the selector
func (m *ModelSelector) String() string {
    if m.ID != "" {
        if m.Provider != "" {
            return fmt.Sprintf("%s@%s", m.ID, m.Provider)
        }
        return m.ID
    }
    if len(m.Tags) > 0 {
        if m.Provider != "" {
            return fmt.Sprintf("tags:%v@%s", m.Tags, m.Provider)
        }
        return fmt.Sprintf("tags:%v", m.Tags)
    }
    return "<empty>"
}
```

**Tests** (add to `internal/llm/models/types_test.go` or new file):

```go
func TestModelSelector_ValidateAvailability(t *testing.T) {
    tests := []struct {
        name      string
        selector  *ModelSelector
        ctx       *RuntimeValidationContext
        wantErr   bool
        errContains string
    }{
        {
            name:     "nil selector",
            selector: nil,
            ctx:      &RuntimeValidationContext{AvailableProviders: []string{"anthropic"}},
            wantErr:  true,
            errContains: "nil",
        },
        {
            name:     "empty selector",
            selector: &ModelSelector{},
            ctx:      &RuntimeValidationContext{AvailableProviders: []string{"anthropic"}},
            wantErr:  true,
            errContains: "must have",
        },
        {
            name:     "local model without endpoint",
            selector: &ModelSelector{Tags: []string{"local"}},
            ctx:      &RuntimeValidationContext{LocalBaseURL: ""},
            wantErr:  true,
            errContains: "local endpoint",
        },
        {
            name:     "local model with endpoint",
            selector: &ModelSelector{Tags: []string{"local"}},
            ctx:      &RuntimeValidationContext{LocalBaseURL: "http://localhost:11434/v1"},
            wantErr:  false,
        },
        {
            name:     "local provider without endpoint",
            selector: &ModelSelector{ID: "my-model", Provider: "local"},
            ctx:      &RuntimeValidationContext{LocalBaseURL: ""},
            wantErr:  true,
            errContains: "local endpoint",
        },
        {
            name:     "cloud model no providers",
            selector: &ModelSelector{Tags: []string{"flagship"}},
            ctx:      &RuntimeValidationContext{AvailableProviders: []string{}},
            wantErr:  true,
            errContains: "no API keys",
        },
        {
            name:     "valid cloud model",
            selector: &ModelSelector{ID: "claude-4.5-opus"},
            ctx:      &RuntimeValidationContext{AvailableProviders: []string{"anthropic"}},
            wantErr:  false,
        },
        {
            name:     "unknown model ID",
            selector: &ModelSelector{ID: "not-a-real-model"},
            ctx:      &RuntimeValidationContext{AvailableProviders: []string{"anthropic"}},
            wantErr:  true,
            errContains: "cannot resolve",
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := tt.selector.ValidateAvailability(tt.ctx)
            if tt.wantErr {
                require.Error(t, err)
                if tt.errContains != "" {
                    assert.Contains(t, err.Error(), tt.errContains)
                }
            } else {
                require.NoError(t, err)
            }
        })
    }
}
```

---

## Task 2: Add RuntimeValidator Interface

**File**: `internal/workflow/v3/validation_runtime.go` (new file)

**Dependencies**: None (can start immediately, parallel with Task 1)

**Implementation**:

```go
// Package v3 provides workflow schema types and validation.
package v3

// RuntimeValidationContext provides context for validating workflow inputs
// that require runtime information (e.g., available API keys, local config).
//
// This is passed through the validation chain when validating inputs before
// workflow execution.
type RuntimeValidationContext struct {
    // AvailableProviders lists provider IDs the user has API keys for
    // (e.g., "anthropic", "openai", "gemini", "xai")
    AvailableProviders []string
    
    // LocalBaseURL is the configured local model endpoint, empty if not configured
    LocalBaseURL string
    
    // UserID is the user performing the action, for user-specific validation
    UserID string
}

// RuntimeValidator is an optional interface that Input types can implement
// to perform validation that requires runtime context.
//
// This is separate from ValidateValue() which only validates the value's
// structure and constraints. RuntimeValidator validates that the value
// can actually be used at runtime (e.g., model can be resolved with API keys).
//
// Input types that implement this:
//   - ModelInput: validates model can be resolved with available providers
//
// Future candidates:
//   - ToolsInput: validate MCP servers are accessible
//   - PresetInput: validate preset files exist
type RuntimeValidator interface {
    // ValidateWithContext performs runtime validation on a value.
    // Returns nil if valid, or an error with actionable guidance.
    ValidateWithContext(value interface{}, ctx *RuntimeValidationContext) error
}

// IsRuntimeValidator checks if an Input implements RuntimeValidator.
// Use this to conditionally perform runtime validation.
func IsRuntimeValidator(input Input) bool {
    _, ok := input.(RuntimeValidator)
    return ok
}

// ValidateInputWithContext validates an input value, including runtime validation
// if the input type implements RuntimeValidator.
func ValidateInputWithContext(input Input, value interface{}, ctx *RuntimeValidationContext) error {
    // First, standard value validation
    if err := input.ValidateValue(value); err != nil {
        return err
    }
    
    // Then, runtime validation if supported
    if rv, ok := input.(RuntimeValidator); ok {
        if err := rv.ValidateWithContext(value, ctx); err != nil {
            return err
        }
    }
    
    return nil
}
```

**Update ModelInput** in `internal/workflow/v3/inputs.go`:

```go
// Add import at top
import (
    "github.com/reliant-labs/reliant/internal/llm/models"
)

// Add method to ModelInput (after ValidateValue)

// ValidateWithContext implements RuntimeValidator for ModelInput.
// It validates that the model selector can be resolved with the user's
// configured API keys and providers.
func (i *ModelInput) ValidateWithContext(value interface{}, ctx *RuntimeValidationContext) error {
    // Convert value to ModelSelector
    var selector *models.ModelSelector
    
    switch v := value.(type) {
    case string:
        selector = &models.ModelSelector{ID: v}
    case map[string]interface{}:
        selector = &models.ModelSelector{}
        if id, ok := v["id"].(string); ok {
            selector.ID = id
        }
        if tags, ok := v["tags"].([]interface{}); ok {
            for _, t := range tags {
                if s, ok := t.(string); ok {
                    selector.Tags = append(selector.Tags, s)
                }
            }
        }
        if provider, ok := v["provider"].(string); ok {
            selector.Provider = provider
        }
    case *models.ModelSelector:
        selector = v
    case models.ModelSelector:
        selector = &v
    case *ModelSelector:
        // v3.ModelSelector - convert to models.ModelSelector
        selector = &models.ModelSelector{
            ID:       v.ID,
            Tags:     v.Tags,
            Provider: v.Provider,
        }
    case ModelSelector:
        selector = &models.ModelSelector{
            ID:       v.ID,
            Tags:     v.Tags,
            Provider: v.Provider,
        }
    default:
        return fmt.Errorf("cannot validate model: unexpected type %T", value)
    }
    
    // Convert v3 context to models context
    modelsCtx := &models.RuntimeValidationContext{
        AvailableProviders: ctx.AvailableProviders,
        LocalBaseURL:       ctx.LocalBaseURL,
    }
    
    return selector.ValidateAvailability(modelsCtx)
}
```

---

## Task 3: Create Reflection-Based Model Scanner

**File**: `internal/workflow/v3/validation/model_scanner.go` (new file)

**Dependencies**: None (can start immediately, parallel with Tasks 1-2)

**Implementation**:

```go
package validation

import (
    "reflect"
    
    "github.com/reliant-labs/reliant/internal/llm/models"
    v3 "github.com/reliant-labs/reliant/internal/workflow/v3"
)

// ModelFieldRef represents a model field found on a workflow node.
// Used for pre-execution validation of all model references.
type ModelFieldRef struct {
    // NodeID is the ID of the node containing this model field
    NodeID string
    
    // NodeType is the type of the node (e.g., "call_llm")
    NodeType string
    
    // FieldName is the struct field name (e.g., "Model")
    FieldName string
    
    // Selector is the resolved ModelSelector value, nil if it's a CEL expression
    Selector *models.ModelSelector
    
    // CELExpr is the raw CEL expression if the field contains one (e.g., "{{inputs.model}}")
    // Empty if Selector is set.
    CELExpr string
}

// modelSelectorType is cached for reflection comparison
var modelSelectorType = reflect.TypeOf((*models.ModelSelector)(nil))

// ScanWorkflowModels finds all model references in a workflow.
// It uses reflection to find *models.ModelSelector fields on any node type,
// making it automatically future-proof for new node types.
//
// Returns a slice of ModelFieldRef, one for each model field found.
// The Selector field is populated if the model is statically defined,
// or CELExpr is populated if it's a template expression.
func ScanWorkflowModels(wf *v3.Workflow) []ModelFieldRef {
    if wf == nil {
        return nil
    }
    
    var refs []ModelFieldRef
    
    // Scan all nodes
    for _, node := range wf.Nodes {
        nodeRefs := findModelFieldsOnNode(node)
        refs = append(refs, nodeRefs...)
        
        // Handle inline workflows (recursive)
        if inlineWf := getInlineWorkflow(node); inlineWf != nil {
            inlineRefs := ScanWorkflowModels(inlineWf)
            // Prefix node IDs with parent for context
            for i := range inlineRefs {
                inlineRefs[i].NodeID = node.GetID() + "." + inlineRefs[i].NodeID
            }
            refs = append(refs, inlineRefs...)
        }
    }
    
    return refs
}

// findModelFieldsOnNode uses reflection to find all *models.ModelSelector fields on a node.
func findModelFieldsOnNode(node v3.Node) []ModelFieldRef {
    var refs []ModelFieldRef
    
    nodeID := node.GetID()
    nodeType := string(node.GetType())
    
    v := reflect.ValueOf(node)
    if v.Kind() == reflect.Ptr {
        if v.IsNil() {
            return refs
        }
        v = v.Elem()
    }
    
    if v.Kind() != reflect.Struct {
        return refs
    }
    
    t := v.Type()
    for i := 0; i < t.NumField(); i++ {
        field := t.Field(i)
        fieldValue := v.Field(i)
        
        // Check if field is *models.ModelSelector
        if field.Type == modelSelectorType {
            ref := ModelFieldRef{
                NodeID:    nodeID,
                NodeType:  nodeType,
                FieldName: field.Name,
            }
            
            if !fieldValue.IsNil() {
                ref.Selector = fieldValue.Interface().(*models.ModelSelector)
            }
            
            refs = append(refs, ref)
        }
        
        // Also check for embedded structs (but skip NodeBase)
        if field.Anonymous && field.Type.Kind() == reflect.Struct && field.Name != "NodeBase" {
            // Recursively check embedded struct
            embeddedRefs := findModelFieldsInStruct(fieldValue, nodeID, nodeType)
            refs = append(refs, embeddedRefs...)
        }
    }
    
    return refs
}

// findModelFieldsInStruct recursively finds model fields in a struct value.
func findModelFieldsInStruct(v reflect.Value, nodeID, nodeType string) []ModelFieldRef {
    var refs []ModelFieldRef
    
    if v.Kind() == reflect.Ptr {
        if v.IsNil() {
            return refs
        }
        v = v.Elem()
    }
    
    if v.Kind() != reflect.Struct {
        return refs
    }
    
    t := v.Type()
    for i := 0; i < t.NumField(); i++ {
        field := t.Field(i)
        fieldValue := v.Field(i)
        
        if field.Type == modelSelectorType {
            ref := ModelFieldRef{
                NodeID:    nodeID,
                NodeType:  nodeType,
                FieldName: field.Name,
            }
            
            if !fieldValue.IsNil() {
                ref.Selector = fieldValue.Interface().(*models.ModelSelector)
            }
            
            refs = append(refs, ref)
        }
    }
    
    return refs
}

// getInlineWorkflow extracts an inline workflow from a node if present.
// Returns nil if the node doesn't have an inline workflow.
func getInlineWorkflow(node v3.Node) *v3.Workflow {
    // Use reflection to check for Inline field
    v := reflect.ValueOf(node)
    if v.Kind() == reflect.Ptr {
        if v.IsNil() {
            return nil
        }
        v = v.Elem()
    }
    
    if v.Kind() != reflect.Struct {
        return nil
    }
    
    inlineField := v.FieldByName("Inline")
    if !inlineField.IsValid() {
        return nil
    }
    
    // Check if it's *v3.Workflow
    if inlineField.Kind() == reflect.Ptr && !inlineField.IsNil() {
        if wf, ok := inlineField.Interface().(*v3.Workflow); ok {
            return wf
        }
    }
    
    return nil
}
```

**Tests** (add to `internal/workflow/v3/validation/model_scanner_test.go`):

```go
package validation

import (
    "testing"
    
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    
    "github.com/reliant-labs/reliant/internal/llm/models"
    v3 "github.com/reliant-labs/reliant/internal/workflow/v3"
)

func TestScanWorkflowModels(t *testing.T) {
    t.Run("finds model on call_llm node", func(t *testing.T) {
        wf := &v3.Workflow{
            Name: "test",
            Nodes: v3.NodeList{
                &v3.CallLLMNode{
                    NodeBase: v3.NodeBase{ID: "agent", Type: "call_llm"},
                    Model:    &models.ModelSelector{Tags: []string{"flagship"}},
                },
            },
        }
        
        refs := ScanWorkflowModels(wf)
        require.Len(t, refs, 1)
        assert.Equal(t, "agent", refs[0].NodeID)
        assert.Equal(t, "call_llm", refs[0].NodeType)
        assert.Equal(t, "Model", refs[0].FieldName)
        assert.NotNil(t, refs[0].Selector)
        assert.Equal(t, []string{"flagship"}, refs[0].Selector.Tags)
    })
    
    t.Run("finds nil model field", func(t *testing.T) {
        wf := &v3.Workflow{
            Name: "test",
            Nodes: v3.NodeList{
                &v3.CallLLMNode{
                    NodeBase: v3.NodeBase{ID: "agent", Type: "call_llm"},
                    Model:    nil, // No model set
                },
            },
        }
        
        refs := ScanWorkflowModels(wf)
        require.Len(t, refs, 1)
        assert.Equal(t, "agent", refs[0].NodeID)
        assert.Nil(t, refs[0].Selector)
    })
    
    t.Run("handles multiple nodes", func(t *testing.T) {
        wf := &v3.Workflow{
            Name: "test",
            Nodes: v3.NodeList{
                &v3.CallLLMNode{
                    NodeBase: v3.NodeBase{ID: "agent1", Type: "call_llm"},
                    Model:    &models.ModelSelector{ID: "claude-4.5-opus"},
                },
                &v3.CallLLMNode{
                    NodeBase: v3.NodeBase{ID: "agent2", Type: "call_llm"},
                    Model:    &models.ModelSelector{Tags: []string{"fast"}},
                },
            },
        }
        
        refs := ScanWorkflowModels(wf)
        require.Len(t, refs, 2)
    })
    
    t.Run("ignores nodes without model fields", func(t *testing.T) {
        wf := &v3.Workflow{
            Name: "test",
            Nodes: v3.NodeList{
                &v3.ExecuteToolsNode{
                    NodeBase: v3.NodeBase{ID: "tools", Type: "execute_tools"},
                },
            },
        }
        
        refs := ScanWorkflowModels(wf)
        assert.Empty(t, refs)
    })
    
    t.Run("nil workflow returns empty", func(t *testing.T) {
        refs := ScanWorkflowModels(nil)
        assert.Empty(t, refs)
    })
}
```

---

## Task 4: Wire Up Validation in chat.go

**File**: `internal/grpc/services/chat.go`

**Dependencies**: Tasks 1, 2, 3 must be complete

**Implementation**:

Add a new function and call it before `ExecuteWorkflow`:

```go
// Add imports
import (
    "github.com/reliant-labs/reliant/internal/llm/models"
    "github.com/reliant-labs/reliant/internal/workflow/v3/validation"
)

// validateModelsAvailable validates that all model references in a workflow
// can be resolved with the user's configured API keys and providers.
//
// This catches model resolution errors BEFORE the workflow starts, providing
// clear error messages instead of failing deep inside workflow execution.
func (s *ChatService) validateModelsAvailable(
    ctx context.Context,
    userID string,
    wf *v3.Workflow,
    inputs map[string]interface{},
) error {
    // Build runtime validation context
    availableDrivers := drivers.GetAvailableDrivers(ctx, userID)
    
    var availableProviders []string
    for driverID, driverConfig := range availableDrivers.Drivers {
        if driverConfig.Enabled && driverConfig.APIKey != "" {
            availableProviders = append(availableProviders, string(driverID))
        }
    }
    
    // Get local config
    var localBaseURL string
    // TODO: Get from project config - need to pass this through or look it up
    // For now, check if "local" is in available providers
    if _, hasLocal := availableDrivers.Drivers["local"]; hasLocal {
        // Local provider is configured
        localBaseURL = "configured" // Placeholder - actual URL not needed for validation
    }
    
    runtimeCtx := &models.RuntimeValidationContext{
        AvailableProviders: availableProviders,
        LocalBaseURL:       localBaseURL,
    }
    
    // Scan workflow for model references
    modelRefs := validation.ScanWorkflowModels(wf)
    
    var errs []string
    for _, ref := range modelRefs {
        selector := ref.Selector
        
        // If selector is nil, the model might be set via CEL expression
        // Try to evaluate it from inputs
        if selector == nil {
            // For now, skip CEL expressions - they'll be validated at runtime
            // TODO: Evaluate CEL expressions with inputs to get the resolved value
            continue
        }
        
        if err := selector.ValidateAvailability(runtimeCtx); err != nil {
            errs = append(errs, fmt.Sprintf("node '%s': %s", ref.NodeID, err.Error()))
        }
    }
    
    if len(errs) > 0 {
        return fmt.Errorf("model validation failed:\n  - %s", strings.Join(errs, "\n  - "))
    }
    
    return nil
}
```

**Call sites** - Add validation before `ExecuteWorkflow` in:

1. `CreateChat` (around line 879):
```go
// Before: workflowRun, err := s.tempClient.ExecuteWorkflow(...)

// Add model validation
if err := s.validateModelsAvailable(ctx, userID, wf, initialData); err != nil {
    return nil, connect.NewError(connect.CodeInvalidArgument, err)
}

workflowRun, err := s.tempClient.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
```

2. `SendMessage` (around line 1782):
```go
// Before: workflowRun, err := s.tempClient.ExecuteWorkflow(...)

// Add model validation (need to load workflow first)
if err := s.validateModelsAvailable(ctx, chat.UserID, wf, initialData); err != nil {
    return nil, connect.NewError(connect.CodeInvalidArgument, err)
}

workflowRun, err := s.tempClient.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
```

3. `ResurrectGhostWorkflow` (around line 495):
```go
// Add similar validation
```

**Note**: The workflow (`wf`) needs to be loaded before validation. Look at existing code patterns for `loadWorkflowForValidation` function.

---

## Task 5: Improve Error Messages in call_llm Activity

**File**: `internal/workflow/v2/activities/handlers/call_llm.go`

**Dependencies**: None (can be done in parallel)

**Implementation**:

Update the error at line 214-216:

```go
// Before:
if node.Model == nil {
    return CallLLMOutput{}, fmt.Errorf("model is required - must be provided via workflow inputs")
}

// After:
if node.Model == nil {
    return CallLLMOutput{}, fmt.Errorf(
        "model is required for node '%s' - must be provided via workflow inputs; "+
        "check that your workflow defines a model input and passes it to this node",
        node.GetID(),
    )
}
```

Also improve the resolution error at line 244-246:

```go
// Before:
resolved, err := registry.Resolve(*node.Model, availableProviders)
if err != nil {
    return CallLLMOutput{}, fmt.Errorf("failed to resolve model: %w. Please check your API key configuration in Settings", err)
}

// After:
resolved, err := registry.Resolve(*node.Model, availableProviders)
if err != nil {
    return CallLLMOutput{}, fmt.Errorf(
        "failed to resolve model %s for node '%s': %w; "+
        "check your API key configuration in Settings or verify the model ID/tags are correct",
        node.Model.String(), node.GetID(), err,
    )
}
```

---

## Task 6: Tests

**Dependencies**: All other tasks should be complete

**Test Files**:
- `internal/llm/models/types_test.go` - Task 1 tests (see Task 1 section)
- `internal/workflow/v3/validation/model_scanner_test.go` - Task 3 tests (see Task 3 section)
- `internal/workflow/v3/inputs_test.go` - Add RuntimeValidator tests
- `internal/grpc/services/chat_test.go` - Integration tests

**Integration Test** (add to `internal/grpc/services/chat_test.go` or new file):

```go
func TestValidateModelsAvailable(t *testing.T) {
    // Test cases:
    // 1. Valid model with API key - passes
    // 2. Valid model without API key - fails with helpful error
    // 3. Invalid model ID - fails with helpful error
    // 4. Local model with local configured - passes
    // 5. Local model without local configured - fails with helpful error
    // 6. Multiple models, one invalid - fails listing the invalid one
}
```

---

## Verification Checklist

After implementation, verify:

- [ ] `go build ./...` succeeds
- [ ] `go test ./internal/llm/models/...` passes
- [ ] `go test ./internal/workflow/v3/...` passes
- [ ] `go test ./internal/grpc/services/...` passes
- [ ] Manual test: Create chat with invalid model ID → get clear error before workflow starts
- [ ] Manual test: Create chat with valid model but no API key → get clear error mentioning API key
- [ ] Manual test: Create chat with local model but no local endpoint → get clear error
- [ ] Manual test: Create chat with valid model and API key → workflow starts normally

---

## Notes for Implementers

1. **Type Imports**: Be careful with the two `ModelSelector` types:
   - `internal/llm/models.ModelSelector` - used in nodes and runtime
   - `internal/workflow/v3.ModelSelector` - used in input schema definitions
   
2. **Reflection**: The model scanner uses reflection to be future-proof. If you add a new node type with a `*models.ModelSelector` field, it will automatically be validated.

3. **CEL Expressions**: The current implementation skips CEL expression evaluation. A future enhancement could evaluate expressions like `{{inputs.model}}` with the provided inputs to get the resolved selector.

4. **Local Config**: The chat.go validation needs access to the local provider config. Look at how `projectConfig` is used elsewhere in the codebase.

5. **Error Messages**: Keep error messages actionable - tell the user what to do to fix the problem.
