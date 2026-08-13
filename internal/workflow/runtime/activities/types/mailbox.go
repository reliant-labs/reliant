// Copyright (c) 2025 Reliant Labs
package types

// The workflow engine dispatches DrainAgentMessages and EmitStreamFinalized as
// LOCAL activities, and a local activity's arguments are handed to the
// registered function by REFLECTION rather than through the data converter
// (sdk/internal/internal_worker.go executeFunction: fnValue.Call(reflectArgs)).
// A map[string]interface{} that a regular activity would happily decode into
// the handler's struct therefore panics as a local activity — the value must
// already BE the registered parameter type.
//
// That forces these structs to live somewhere both sides can name, and the
// import graph only allows one such place: handlers imports runtime, so runtime
// cannot import handlers. This package is the existing seam for exactly that
// (see ActivityInput), so the definitions live here and handlers re-exports
// them as type ALIASES — aliases, not defined types, so the reflect.Type the
// workflow passes is identical to the one the registered function expects.

// DrainAgentMessagesInput is the input for the DrainAgentMessages activity.
type DrainAgentMessagesInput struct {
	ChatID string `json:"chat_id" reliant:"-"`
	// Thread is the RECIPIENT thread — the one about to resume its loop and
	// whose mailbox should be drained before its next call_llm.
	Thread string `json:"thread"`
}

// DrainAgentMessagesOutput reports what was delivered.
type DrainAgentMessagesOutput struct {
	Count       int  `json:"count"`
	HasMessages bool `json:"has_messages"`
}

// EmitStreamFinalizedInput is the input for the EmitStreamFinalized activity.
type EmitStreamFinalizedInput struct {
	ChatID        string `json:"chat_id" reliant:"-"`
	MessageID     string `json:"message_id"`
	Thread        string `json:"thread,omitempty"`
	Reason        string `json:"reason"` // "completed", "aborted", or "cancelled"
	LastStreamSeq int64  `json:"last_stream_seq,omitempty"`
}

// EmitStreamFinalizedOutput is the output for the EmitStreamFinalized activity.
type EmitStreamFinalizedOutput struct {
	Success bool `json:"success"`
}
