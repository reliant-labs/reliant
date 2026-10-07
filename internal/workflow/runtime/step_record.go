// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// stepAttempt is one activity attempt as its step_executions row records it:
// where in the graph it ran, what it was given, and how it ended. It is what a
// run is debugged from (the builder's Run tab), so it carries the inputs and
// the error as well as the output.
type stepAttempt struct {
	WorkflowID   string
	StepID       string
	ActivityType string
	// Scope is the node's loop scope and dotted graph position.
	Scope activityInputInfo
	// Attempt is Temporal's 1-based attempt number; 0 when unknown.
	Attempt int
	// Args is the node's resolved config (recordedArgs), when it is a graph node.
	Args       sql.NullString
	Output     interface{}
	Err        error
	DurationMs int64
}

const (
	// recordedArgsMaxBytes bounds one row's input_json. Inputs are recorded
	// for every graph step of every run, so a write-file tool call must not
	// put its whole file in the table a second time.
	recordedArgsMaxBytes = 64 << 10
	// recordedErrorMaxBytes bounds error_message: wrapped errors from a
	// provider can carry a whole response body.
	recordedErrorMaxBytes = 16 << 10
)

// recordedStringLimits are the per-string caps tried in turn until the args
// fit recordedArgsMaxBytes: generous first, so a prompt reads whole when it can.
var recordedStringLimits = []int{4 << 10, 512}

// recordedArgs is what a graph step was given: its node's args AFTER every
// {{ }} expression was evaluated, which is the input the activity actually ran
// with. The builder shows it as the step's resolved inputs.
//
// Only graph nodes have args to record. Infrastructure activities take plain
// maps, and a save_message node's args are the message itself, already stored
// as the message — neither is recorded. Long strings are cut and marked rather
// than dropping the whole record, so the shape of the input survives.
func recordedArgs(input interface{}) sql.NullString {
	activityInput, ok := input.(types.ActivityInput)
	if !ok || activityInput.Node == nil || activityInput.Node.GetType() == model.NodeTypeSaveMessage {
		return sql.NullString{}
	}
	args, err := model.NodeArgsAsMap(activityInput.Node)
	if err != nil || len(args) == 0 {
		return sql.NullString{}
	}
	return boundedJSON(args, recordedArgsMaxBytes)
}

// boundedJSON marshals value within maxBytes, cutting long strings first and
// recording a marker when even that does not fit.
func boundedJSON(value interface{}, maxBytes int) sql.NullString {
	for _, limit := range recordedStringLimits {
		encoded, err := json.Marshal(truncateLongStrings(value, limit))
		if err != nil {
			return sql.NullString{}
		}
		if len(encoded) <= maxBytes {
			return sql.NullString{String: string(encoded), Valid: true}
		}
	}
	return sql.NullString{String: `{"_truncated":"too large to record"}`, Valid: true}
}

// truncateLongStrings returns a copy of a decoded-JSON value with every string
// longer than limit bytes cut to limit (on a rune boundary) and marked with how
// much was dropped.
func truncateLongStrings(value interface{}, limit int) interface{} {
	switch typed := value.(type) {
	case string:
		return truncateString(typed, limit)
	case map[string]interface{}:
		copied := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			copied[key] = truncateLongStrings(item, limit)
		}
		return copied
	case []interface{}:
		copied := make([]interface{}, len(typed))
		for index, item := range typed {
			copied[index] = truncateLongStrings(item, limit)
		}
		return copied
	default:
		return value
	}
}

func truncateString(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return fmt.Sprintf("%s… [%d more characters]", text[:cut], utf8.RuneCountInString(text[cut:]))
}

// recordedError is the error_message a failed attempt records.
func recordedError(err error) sql.NullString {
	if err == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: truncateString(err.Error(), recordedErrorMaxBytes), Valid: true}
}
