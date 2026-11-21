package workflow

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
)

// ValidateWorkflow validates a workflow without saving it.
//
// It validates the definition in the REQUEST, converted exactly as
// SaveWorkflow converts it for storage, so the findings describe what a save
// of that canvas would store. That is what lets the builder show problems
// while the edits are still unsaved. (It used to validate the stored draft
// instead, which reported on the last save, not on the canvas.)
//
// A built-in is read-only and cannot be shadowed by a user workflow (saves
// reject a built-in's slug), so a built-in is validated from its embedded
// file.
func (s *Service) ValidateWorkflow(
	ctx context.Context,
	req *connect.Request[reliantv1.ValidateWorkflowRequest],
) (*connect.Response[reliantv1.ValidateWorkflowResponse], error) {
	protoWf := req.Msg.Workflow
	if protoWf == nil {
		return nil, svcerr.InvalidArgument("workflow is required")
	}

	userID := auth.MustGetUserID(ctx)

	builtinName := strings.TrimPrefix(protoWf.Name, "builtin://")
	yamlBytes, builtinErr := builtin.BuiltinWorkflowsFS.ReadFile(builtinName + ".yaml")
	if builtinErr != nil {
		var err error
		yamlBytes, err = rpcWorkflowToYAML(protoWf)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to marshal workflow to YAML"))
		}
	}

	// Errors and warnings both: warnings (type "warning:<category>") are the
	// stand-in-zero and loose-schema findings an author should still see.
	check := s.validateWorkflowDefinition(ctx, userID, yamlBytes)
	return connect.NewResponse(&reliantv1.ValidateWorkflowResponse{
		Valid:  check.valid(),
		Errors: check.protoErrors(true),
	}), nil
}
