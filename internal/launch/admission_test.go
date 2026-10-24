package launch

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

type admissionStore struct{ WorkflowResolver }

func (admissionStore) GetProjectWorkflowsJSON(context.Context, string) (*string, error) {
	return nil, nil
}
func (admissionStore) GetUsableWorkflowBySlug(_ context.Context, _, slug string) (*db.WorkflowDraft, error) {
	if slug == "root" {
		return &db.WorkflowDraft{Definition: "name: root\nentry: [child]\nnodes:\n  - {id: child, type: workflow, ref: child}\n"}, nil
	}
	return nil, errors.New("test store unavailable")
}

func TestAdmissionChildLookupFailureRemainsRetryable(t *testing.T) {
	_, err := ResolveValidatedRunWorkflow(context.Background(), admissionStore{}, "user", "root", "project")
	var lookup *WorkflowLookupError
	require.ErrorAs(t, err, &lookup, "child store failure must not become a permanent invalid-definition verdict")
	require.Contains(t, lookup.Error(), "test store unavailable")
}
