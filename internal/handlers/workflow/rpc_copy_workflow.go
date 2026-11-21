package workflow

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// CopyWorkflow creates a copy of an existing workflow with a new unique name
func (s *Service) CopyWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.CopyWorkflowRequest],
) (*connect.Response[reliantv1.CopyWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if req.Msg.SourceSlug == "" {
		return nil, svcerr.InvalidArgument("source_slug is required")
	}

	// Load the source workflow (checks builtin, project, and user workflows)
	getReq := connect.NewRequest(&reliantv1.GetWorkflowRequest{
		ProjectId:  req.Msg.ProjectId,
		Name:       req.Msg.SourceSlug,
		WorktreeId: req.Msg.WorktreeId,
	})
	getReq.Header().Set("Authorization", req.Header().Get("Authorization"))
	getResp, err := s.GetWorkflow(ctx, getReq)
	if err != nil {
		return nil, err // Error already formatted
	}

	sourceWf := getResp.Msg.Workflow
	if sourceWf == nil {
		return nil, svcerr.NotFound("source workflow")
	}

	// Generate new name with unique suffix
	var newName string
	if req.Msg.NewName != nil && *req.Msg.NewName != "" {
		newName = *req.Msg.NewName
	} else {
		// Auto-generate name with random suffix
		newName = sourceWf.Name + "-" + uuid.New().String()[:6]
	}

	// Create the copy without copying protobuf internal lock state.
	copyWf := proto.Clone(sourceWf).(*reliantv1.Workflow)
	copyWf.Name = newName

	// Save the copy using SaveWorkflow
	saveReq := connect.NewRequest(&reliantv1.SaveWorkflowRequest{
		ProjectId: req.Msg.ProjectId,
		Workflow:  copyWf,
	})
	saveReq.Header().Set("Authorization", req.Header().Get("Authorization"))
	saveResp, err := s.SaveWorkflow(ctx, saveReq)
	if err != nil {
		return nil, err
	}

	// Track the fork origin in the database
	if saveResp.Msg.Id != "" {
		// Record where this workflow was forked from
		forkedFrom := fmt.Sprintf("%s:%s", getResp.Msg.Source, req.Msg.SourceSlug)
		_, err := s.deps.Repo.UpdateWorkflowForkedFrom(ctx, saveResp.Msg.Id, forkedFrom)
		if err != nil {
			logging.WarnContext(ctx, "Failed to set forked_from on copied workflow", "error", err)
			// Don't fail the copy operation for this
		}
	}

	return connect.NewResponse(&reliantv1.CopyWorkflowResponse{
		Success:  true,
		Message:  fmt.Sprintf("Workflow copied successfully as '%s'", newName),
		Workflow: saveResp.Msg.Workflow,
		Slug:     saveResp.Msg.Slug,
		Id:       saveResp.Msg.Id,
	}), nil
}
