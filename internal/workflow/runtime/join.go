// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// JoinState tracks the state of all join nodes in a workflow
type JoinState struct {
	// Progress maps join step ID to its progress
	Progress map[string]*JoinProgress
}

// JoinProgress tracks completion status for a single join node
type JoinProgress struct {
	// Sources are the step IDs this join is waiting for (derived from incoming edges)
	Sources []string

	// Completed tracks which sources have completed
	Completed map[string]bool

	// Skipped tracks which sources were skipped (condition was false)
	Skipped map[string]bool

	// Results stores the output from each completed source
	Results map[string]interface{}

	// StartedAt is when the first source completed
	StartedAt time.Time

	// Triggered indicates whether this join has already fired its completion event
	Triggered bool
}

// BuildJoinSources constructs the sources list for CEL evaluation.
// Skipped sources are treated as completed to simplify CEL conditions.
func (jp *JoinProgress) BuildJoinSources() []map[string]interface{} {
	sources := make([]map[string]interface{}, 0, len(jp.Sources))
	for _, sourceID := range jp.Sources {
		source := map[string]interface{}{
			"id":        sourceID,
			"status":    "pending",
			"output":    nil,
			"completed": false,
		}

		if jp.Completed[sourceID] {
			source["status"] = "completed"
			source["completed"] = true
			if output, ok := jp.Results[sourceID].(map[string]interface{}); ok {
				source["output"] = output
			}
		} else if jp.Skipped[sourceID] {
			// Treat skipped as completed for simplified CEL evaluation
			source["status"] = "completed"
			source["completed"] = true
			source["output"] = model.SkippedOutputMap()
		}

		sources = append(sources, source)
	}
	return sources
}

// NewJoinState creates a new JoinState
func NewJoinState() *JoinState {
	return &JoinState{
		Progress: make(map[string]*JoinProgress),
	}
}

// InitializeJoins scans the workflow for join steps and initializes their progress
// by looking at incoming edges to determine sources
func (js *JoinState) InitializeJoins(workflow *reliantv1.Workflow) {
	// Build map of step ID -> incoming edge sources
	incomingEdges := make(map[string][]string)
	for _, edge := range workflow.GetEdges() {
		// Normalize edge source (strip event suffix if present)
		sourceStepID := normalizeEdgeFrom(edge.GetFrom())
		// Each case target is an incoming edge
		for _, edgeCase := range edge.GetCases() {
			for _, to := range edgeCase.GetTo() {
				incomingEdges[to] = append(incomingEdges[to], sourceStepID)
			}
		}
		// Default target is also an incoming edge
		for _, to := range edge.GetDefault() {
			incomingEdges[to] = append(incomingEdges[to], sourceStepID)
		}
	}

	// Initialize progress for each join step
	for _, step := range workflow.GetNodes() {
		if model.NodeType(step) == model.NodeTypeJoin {
			sources := incomingEdges[model.NodeID(step)]
			// Deduplicate sources (same step could have multiple edges)
			uniqueSources := deduplicateSources(sources)

			js.Progress[model.NodeID(step)] = &JoinProgress{
				Sources:   uniqueSources,
				Completed: make(map[string]bool),
				Skipped:   make(map[string]bool),
				Results:   make(map[string]interface{}),
			}
		}
	}
}

// deduplicateSources removes duplicate step IDs from sources
func deduplicateSources(sources []string) []string {
	seen := make(map[string]bool)
	unique := make([]string, 0, len(sources))
	for _, s := range sources {
		if !seen[s] {
			seen[s] = true
			unique = append(unique, s)
		}
	}
	return unique
}

// RecordCompletion records that a source step has completed.
// The now parameter must come from workflow.Now(ctx) for determinism.
// Returns the join step IDs that were updated, in sorted order for deterministic replay.
func (js *JoinState) RecordCompletion(sourceStepID string, output interface{}, now time.Time) []string {
	var satisfiedJoins []string

	// Sort join IDs for deterministic iteration order
	joinIDs := make([]string, 0, len(js.Progress))
	for joinID := range js.Progress {
		joinIDs = append(joinIDs, joinID)
	}
	sort.Strings(joinIDs)

	for _, joinID := range joinIDs {
		progress := js.Progress[joinID]
		// Skip already triggered joins
		if progress.Triggered {
			continue
		}

		// Check if this source is relevant to this join
		isSource := false
		for _, src := range progress.Sources {
			if src == sourceStepID {
				isSource = true
				break
			}
		}

		if !isSource {
			continue
		}

		// Record start time
		if progress.StartedAt.IsZero() {
			progress.StartedAt = now
		}

		// Check if this is a skip event
		if model.IsSkippedOutput(output) {
			progress.Skipped[sourceStepID] = true
			progress.Results[sourceStepID] = output
			satisfiedJoins = append(satisfiedJoins, joinID)
			continue
		}

		// Record completion
		progress.Completed[sourceStepID] = true
		progress.Results[sourceStepID] = output

		satisfiedJoins = append(satisfiedJoins, joinID)
	}

	return satisfiedJoins
}

// IsJoinSatisfied checks if a join's condition is met based on the condition expression.
// Supports shorthand: "all" (default), "any", or full CEL expressions.
func (js *JoinState) IsJoinSatisfied(joinID string, condition string) bool {
	progress, exists := js.Progress[joinID]
	if !exists || progress.Triggered {
		return false
	}

	sources := progress.BuildJoinSources()
	result, err := EvaluateJoinCondition(condition, sources)
	if err != nil {
		// On error, fall back to "all" logic for safety
		completedCount := len(progress.Completed)
		skippedCount := len(progress.Skipped)
		totalSources := len(progress.Sources)
		return (completedCount + skippedCount) >= totalSources
	}
	return result
}

// expandJoinCondition expands shorthand conditions to full CEL expressions.
// Only "all" and "any" are valid conditions (validated at parse time).
// Any other value defaults to "all" as a safeguard.
func expandJoinCondition(condition string) string {
	switch strings.TrimSpace(strings.ToLower(condition)) {
	case "any":
		return "sources.exists(s, s.status == 'completed')"
	default:
		// Empty, "all", or any other value defaults to "all"
		return "sources.all(s, s.status == 'completed')"
	}
}

// EvaluateJoinCondition evaluates a join condition against join sources.
// Only "all" and "any" are supported:
//   - "all" (or empty): All sources must complete
//   - "any": At least one source must complete
//
// Skipped sources are treated as completed for join satisfaction.
func EvaluateJoinCondition(condition string, sources []map[string]interface{}) (bool, error) {
	expanded := expandJoinCondition(condition)

	// Create CEL environment with sources variable
	env, err := cel.NewEnv(
		cel.StdLib(),
		cel.Variable("sources", cel.ListType(cel.MapType(cel.StringType, cel.DynType))),
	)
	if err != nil {
		return false, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	// Parse and check the expression
	ast, issues := env.Compile(expanded)
	if issues != nil && issues.Err() != nil {
		return false, fmt.Errorf("failed to compile CEL expression: %w", issues.Err())
	}

	// Create the program
	prg, err := env.Program(ast)
	if err != nil {
		return false, fmt.Errorf("failed to create CEL program: %w", err)
	}

	// Evaluate
	out, _, err := prg.Eval(map[string]interface{}{
		"sources": sources,
	})
	if err != nil {
		return false, fmt.Errorf("failed to evaluate CEL expression: %w", err)
	}

	// Convert result to bool
	result, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("CEL expression did not return bool, got %T", out.Value())
	}

	return result, nil
}

// MarkTriggered marks a join as having fired its completion event
func (js *JoinState) MarkTriggered(joinID string) {
	if progress, exists := js.Progress[joinID]; exists {
		progress.Triggered = true
	}
}

// GetJoinOutputProto builds proto join output for a completed join.
// Uses JoinOutput as the canonical contract representation.
func (js *JoinState) GetJoinOutputProto(joinID string) *reliantv1.JoinOutput {
	progress, exists := js.Progress[joinID]
	if !exists {
		return nil
	}

	sources := progress.BuildJoinSources()
	protoSources := make([]*structpb.Struct, 0, len(sources))
	for _, source := range sources {
		protoSource, err := structpb.NewStruct(source)
		if err != nil {
			continue
		}
		protoSources = append(protoSources, protoSource)
	}

	return &reliantv1.JoinOutput{Sources: protoSources}
}

// joinOutputProtoToMap converts proto JoinOutput to CEL/runtime map representation.
func joinOutputProtoToMap(joinOutput *reliantv1.JoinOutput) map[string]interface{} {
	if joinOutput == nil {
		return nil
	}

	sources := make([]map[string]interface{}, 0, len(joinOutput.GetSources()))
	for _, source := range joinOutput.GetSources() {
		if source == nil {
			sources = append(sources, map[string]interface{}{})
			continue
		}
		sources = append(sources, source.AsMap())
	}

	return model.JoinOutputToMap(sources)
}

// GetJoinOutput builds the flattened output map used in CEL/runtime contexts.
func (js *JoinState) GetJoinOutput(joinID string) map[string]interface{} {
	return joinOutputProtoToMap(js.GetJoinOutputProto(joinID))
}

// GetJoinSources returns the sources for a join step
func (js *JoinState) GetJoinSources(joinID string) []string {
	if progress, exists := js.Progress[joinID]; exists {
		return progress.Sources
	}
	return nil
}

// String returns a debug string for the join state.
// Output is sorted by join ID for deterministic logging.
func (js *JoinState) String() string {
	if js == nil || len(js.Progress) == 0 {
		return "JoinState{empty}"
	}

	joinIDs := make([]string, 0, len(js.Progress))
	for joinID := range js.Progress {
		joinIDs = append(joinIDs, joinID)
	}
	sort.Strings(joinIDs)

	result := "JoinState{\n"
	for _, joinID := range joinIDs {
		progress := js.Progress[joinID]
		result += fmt.Sprintf("  %s: sources=%v completed=%d/%d triggered=%v\n",
			joinID, progress.Sources, len(progress.Completed), len(progress.Sources), progress.Triggered)
	}
	result += "}"
	return result
}

// Logger interface for processJoinEvents
type joinLogger interface {
	Info(msg string, keyvals ...interface{})
}

// JoinSaveMessageFunc is called when a join completes and has save_message config.
// It receives the join node and its aggregated output.
type JoinSaveMessageFunc func(node *reliantv1.Node, output map[string]interface{})

// processJoinEvents processes events through join nodes.
// It updates join state based on source completions and generates
// synthetic completion events when joins are satisfied.
// If saveMessageFunc is provided, it's called for join nodes with save_message config.
// The now parameter must come from workflow.Now(ctx) for determinism.
func processJoinEvents(
	events []*core.WorkflowEvent,
	joinState *JoinState,
	wf *reliantv1.Workflow,
	workflowID string,
	chatID string,
	workflowName string,
	nodeOutputs map[string]interface{},
	logger joinLogger,
	saveMessageFunc JoinSaveMessageFunc,
	now time.Time,
) []*core.WorkflowEvent {
	// If no join nodes, pass events through unchanged
	if len(joinState.Progress) == 0 {
		return events
	}

	// Build node map for looking up join conditions
	nodeMap := make(map[string]*reliantv1.Node)
	for _, n := range wf.GetNodes() {
		nodeMap[model.NodeID(n)] = n
	}

	// Process each event through join state
	for _, event := range events {
		if event.StepID == "" {
			// Skip workflow start events
			continue
		}

		// Record completion in join state
		// This returns list of join IDs that this event is relevant to (sorted deterministically)
		affectedJoins := joinState.RecordCompletion(event.StepID, event.Data, now)

		for _, joinID := range affectedJoins {
			step := nodeMap[joinID]
			if step == nil {
				continue
			}

			// Check if join is now satisfied using condition
			if joinState.IsJoinSatisfied(joinID, model.ConditionExpr(step)) {
				logger.Info("[Workflow Runtime] Join satisfied",
					"joinID", joinID,
					"condition", model.ConditionExpr(step),
					"sources", joinState.GetJoinSources(joinID))

				// Generate join output and store in nodeOutputs
				joinOutput := joinState.GetJoinOutput(joinID)
				nodeOutputs[joinID] = joinOutput

				// Mark as triggered to prevent re-firing
				joinState.MarkTriggered(joinID)

				// Execute save_message if configured on the join node
				if step.GetSaveMessage() != nil && saveMessageFunc != nil {
					saveMessageFunc(step, joinOutput)
				}

				// Create synthetic completion event for the join
				joinEvent := &core.WorkflowEvent{
					ID:           fmt.Sprintf("join-%s-%d", joinID, now.UnixNano()),
					WorkflowID:   workflowID,
					ChatID:       chatID,
					WorkflowName: workflowName,
					StepID:       joinID,
					Data:         joinOutput,
				}

				// Append join event to be processed in this iteration
				events = append(events, joinEvent)
			}
		}
	}

	return events
}
