package model

// InternalActivities are the activity names whose step output the UI never
// renders, because the work they did is already visible some other way.
//
// This is the server half of a decision the frontend also makes, in
// web/src/components/Chat/thread-views/activityIndicators.ts
// (INTERNAL_ACTIVITIES). The two lists MUST agree, and this comment is the only
// thing that says so, so they are worth changing together:
//
//   - the frontend uses it to decide which steps get an activity indicator —
//     an internal step gets none, because it is plumbing.
//   - the server uses it to decide which steps ship their output_json at all
//     (GetStepExecutionsForChat). An internal step's output is never read, and
//     it is the bulk of the data: on the worst measured chat, 32,013 of 32,023
//     steps are internal, and their output_json totals 82 MB.
//
// If they drift, nothing errors. The symptom is an activity indicator that
// renders with no context, because the frontend decided to show a step whose
// output the server decided not to send — a divergence no test would notice
// unless it was looking for exactly this.
//
// SaveMessage and CallLLM produce messages, and a message is rendered as
// itself. Approval and ExecuteTools are rendered inline by ToolExecution. The
// rest is workflow bookkeeping.
var InternalActivities = []string{
	"WorkflowStatus",    // Internal workflow state management
	"WorkflowError",     // Internal error handling
	"Cleanup",           // Internal cleanup
	"FetchThreadResult", // Internal thread fetching
	"FailStep",          // Internal failure handling
	"SaveMessage",       // Produces messages (rendered as the message itself)
	"CallLLM",           // Produces messages (rendered as the message itself)
	"Approval",          // Approvals rendered inline by ToolExecution
	"ExecuteTools",      // Tool calls rendered inline by ToolExecution
}
