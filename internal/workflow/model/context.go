package model

// IterContext provides iteration context for loop CEL expressions.
type IterContext struct {
	Iteration int         `json:"iteration"`
	Index     int         `json:"index"`
	Item      interface{} `json:"item,omitempty"`
	Key       string      `json:"key,omitempty"`
}

// WorkflowContext provides workflow metadata for CEL expressions.
//
// Path, Branch and WorktreePath describe WHERE the evaluating scope runs, and
// are filled by the runtime from the execution context (see
// runtime.scopeEnvironment). Path is the scope's working directory and is never
// legitimately empty in a run that has one: a reference to workflow.path in a
// run without a directory is an evaluation error, not "" (see
// wfcel.CheckWorkflowPathReference). Branch and WorktreePath are empty when the
// scope is not the chat's own worktree.
//
// ChatID is the chat the run belongs to: the id its page is reached by
// (/workflows/runs/<chat_id>), so a run can link to itself from what it writes
// (a GitHub check run's details_url). Empty where no chat is known (an
// activation's form preview).
type WorkflowContext struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ChatID       string `json:"chat_id"`
	Path         string `json:"path"`
	Branch       string `json:"branch"`
	Mode         string `json:"mode"`
	RunID        string `json:"run_id"`
	WorktreePath string `json:"worktree_path"`
}

// BuildIterContext creates an iteration context map for CEL evaluation.
func BuildIterContext(iteration int) map[string]interface{} {
	return map[string]interface{}{
		"iteration": iteration,
		"index":     iteration,
	}
}

// BuildParallelIterContext creates an iteration context for parallel loop CEL evaluation.
// Includes item (current element), index (position), and optionally key (map key).
func BuildParallelIterContext(index int, item interface{}, key string) map[string]interface{} {
	return map[string]interface{}{
		"iteration": index,
		"index":     index,
		"item":      item,
		"key":       key,
	}
}
