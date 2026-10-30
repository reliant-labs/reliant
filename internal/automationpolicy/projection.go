package automationpolicy

import (
	"encoding/json"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// SafeDefinition is deliberately allowlisted. Params, messages, source/filter
// expressions, preset values and graph literals are arbitrary user data, not
// safe audit material even when the schema did not mark a field secret.
func SafeDefinition(t *core.Trigger) any {
	if t == nil {
		return nil
	}
	return struct {
		ID              string              `json:"id"`
		UserID          string              `json:"user_id"`
		ProjectID       string              `json:"project_id"`
		MachineID       string              `json:"machine_id"`
		ConnectionID    *string             `json:"connection_id,omitempty"`
		Workflow        string              `json:"workflow"`
		DeclaredTrigger *string             `json:"declared_trigger,omitempty"`
		Enabled         bool                `json:"enabled"`
		Identity        core.ActingIdentity `json:"acting_identity"`
		Configuration   string              `json:"configuration"`
	}{t.ID, t.UserID, t.ProjectID, t.DaemonID, t.ConnectionID, t.Workflow, t.WorkflowTrigger, t.Enabled, t.ActingIdentity, "Input values, message, source expressions and graph literals withheld"}
}

const MaxDisplayBytes = 32 * 1024

func SafeJSON(t *core.Trigger) (string, error) {
	data, err := json.Marshal(SafeDefinition(t))
	if err != nil {
		return "", err
	}
	if len(data) > MaxDisplayBytes {
		return `{"configuration":"withheld: display size limit"}`, nil
	}
	return string(data), nil
}
