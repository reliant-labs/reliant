package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// UploadScenario saves a scenario YAML file to the project directory
func (s *Service) UploadScenario(
	ctx context.Context,
	req *connect.Request[reliantv1.UploadScenarioRequest],
) (*connect.Response[reliantv1.UploadScenarioResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if userID == "" {
		return nil, svcerr.Unauthenticated("user not authenticated")
	}

	// Validate inputs
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id required")
	}
	if req.Msg.WorkflowSlug == "" {
		return nil, svcerr.InvalidArgument("workflow_slug required")
	}
	if workflowref.Slug(req.Msg.WorkflowSlug) != req.Msg.WorkflowSlug {
		// It names a directory: <slug>/scenarios/ is only found again under
		// the workflow's slug, and anything else could escape the project.
		return nil, svcerr.InvalidArgument(fmt.Sprintf("workflow_slug %q is not a workflow slug (want %q)", req.Msg.WorkflowSlug, workflowref.Slug(req.Msg.WorkflowSlug)))
	}
	if req.Msg.Filename == "" {
		return nil, svcerr.InvalidArgument("filename required")
	}
	if req.Msg.YamlContent == "" {
		return nil, svcerr.InvalidArgument("yaml_content required")
	}

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	// Sanitize filename - remove extension if provided and any path components
	filename := filepath.Base(req.Msg.Filename)
	filename = strings.TrimSuffix(filename, ".yaml")
	filename = strings.TrimSuffix(filename, ".yml")

	// Validate the YAML is one scenario, read the way every surface reads a
	// project scenario file (wfscenario.ParseScenarioFile).
	scenario, err := wfscenario.ParseScenarioFile([]byte(req.Msg.YamlContent), filename)
	if err != nil {
		return nil, svcerr.InvalidArgument("invalid scenario YAML: " + err.Error())
	}

	project, err := s.deps.ProjectRows.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	if project == nil {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, fmt.Sprintf("project not found: %s", req.Msg.ProjectId))
	}
	if s.deps.Router == nil {
		return nil, svcerr.Unavailable("no daemon router: this server cannot write to the project's machine")
	}
	userID = auth.MustGetUserID(ctx)
	// The file is written into the project's checkout on the machine holding
	// it, at its path there — never projects.path, the creating machine's.
	checkout, err := projectcheckout.Resolve(ctx, s.deps.Repo, s.deps.Router, userID, project, "save a scenario into it")
	if err != nil {
		return nil, err
	}

	// Where the locator (workflowref) reads it back from:
	// .reliant/workflows/<slug>/scenarios/<filename>.yaml.
	filePath := checkout.Join(filepath.Join(filepath.FromSlash(workflowref.Dir), filepath.FromSlash(workflowref.ScenarioPath(req.Msg.WorkflowSlug, filename))))

	// Write the file via daemon (fs.write_file auto-creates parent directories)
	writePayload, _ := json.Marshal(map[string]interface{}{
		"path":    filePath,
		"content": req.Msg.YamlContent,
	})
	if _, err := checkout.Send(ctx, s.deps.Router, userID, "fs.write_file", writePayload, 15000); err != nil {
		return nil, daemonerr.Error("write the scenario file", err)
	}

	// Generate the scenario ID
	scenarioID := fmt.Sprintf("project:%s:%s", req.Msg.WorkflowSlug, filename)

	// Convert to proto for response
	protoScenario, err := scenarioToProtoScenario(scenario, scenarioID, filePath)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to convert scenario"))
	}

	return connect.NewResponse(&reliantv1.UploadScenarioResponse{
		Success:  true,
		Message:  fmt.Sprintf("Scenario saved to %s", filePath),
		Path:     filePath,
		Scenario: protoScenario,
	}), nil
}
