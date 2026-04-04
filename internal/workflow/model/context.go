package model

// IterContext provides iteration context for loop CEL expressions.
type IterContext struct {
	Iteration int         `json:"iteration"`
	Index     int         `json:"index"`
	Item      interface{} `json:"item,omitempty"`
	Key       string      `json:"key,omitempty"`
}

// WorkflowContext provides workflow metadata for CEL expressions.
type WorkflowContext struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	Branch       string `json:"branch"`
	Mode         string `json:"mode"`
	RunID        string `json:"run_id"`
	SessionID    string `json:"session_id"`
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
