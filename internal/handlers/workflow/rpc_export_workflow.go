package workflow

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

// ExportWorkflow exports a workflow from the database as YAML
func (s *Service) ExportWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.ExportWorkflowRequest],
) (*connect.Response[reliantv1.ExportWorkflowResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	if req.Msg.Slug == "" {
		return nil, svcerr.InvalidArgument("slug is required")
	}

	// Handle builtin:// protocol
	if strings.HasPrefix(req.Msg.Slug, "builtin://") {
		builtinName := strings.TrimPrefix(req.Msg.Slug, "builtin://")
		filename := builtinName + ".yaml"
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(filename)
		if err != nil {
			return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("builtin workflow not found: %s", req.Msg.Slug))
		}
		protoWf, err := parseWorkflowYAML(data)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to parse builtin workflow"))
		}
		return connect.NewResponse(&reliantv1.ExportWorkflowResponse{
			Success:     true,
			YamlContent: data,
			Filename:    builtinName + ".yaml",
			Workflow:    protoWf,
		}), nil
	}

	// Look up user's workflow in database
	userID := auth.MustGetUserID(ctx)
	draft, err := s.deps.Drafts.GetWorkflowDraftBySlug(ctx, userID, req.Msg.Slug)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to look up workflow"))
	}

	if draft == nil {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("workflow not found: %s", req.Msg.Slug))
	}

	// Parse the stored definition into RPC proto
	protoWf, err := parseWorkflowYAML([]byte(draft.Definition))
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to parse stored workflow"))
	}

	// Convert back to YAML with proper formatting via v2yaml
	yamlData, err := rpcWorkflowToYAML(protoWf)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to marshal workflow to YAML"))
	}

	return connect.NewResponse(&reliantv1.ExportWorkflowResponse{
		Success:     true,
		YamlContent: yamlData,
		Filename:    draft.Slug + ".yaml",
		Workflow:    protoWf,
	}), nil
}
