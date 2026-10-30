package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/workflowcatalog"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// ============================================================================
// RPC Implementations
// ============================================================================

// ListWorkflows returns all available workflows from multiple sources:
// 1. Builtin workflows (embedded in binary) - lowest priority
// 2. Project workflows (.reliant/workflows/*.yaml files) - read-only, team-shared
// 3. User workflows (stored in database) - highest priority, editable
// User workflows override project workflows with the same slug.
func (s *Service) ListWorkflows(
	ctx context.Context,
	req *connect.Request[reliantv1.ListWorkflowsRequest],
) (*connect.Response[reliantv1.ListWorkflowsResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	userID := auth.MustGetUserID(ctx)
	project, err := s.ownedProject(ctx, req.Msg.ProjectId, userID)
	if err != nil {
		return nil, err
	}

	// Every read the listing makes, made once and concurrently: the user's
	// workflows, the project's synced ones, and the builtin visibility
	// overrides. Validation and machine analysis below resolve every ref
	// against the catalog in memory.
	workflowItemType := int32(reliantv1.HiddenItemType_HIDDEN_ITEM_TYPE_WORKFLOW)
	var (
		catalog          *workflowcatalog.Catalog
		wfOverrides      map[string]bool
		wfHiddenDefaults []string
		overridesErr     error
		hiddenErr        error
		reads            sync.WaitGroup
	)
	reads.Add(3)
	go func() {
		defer reads.Done()
		catalog = workflowcatalog.Load(ctx, s.deps.Repo, userID, project.ID)
	}()
	go func() {
		defer reads.Done()
		wfOverrides, overridesErr = s.deps.Repo.ListVisibilityOverrides(ctx, userID, workflowItemType)
	}()
	go func() {
		defer reads.Done()
		wfHiddenDefaults, hiddenErr = s.deps.Repo.ListHiddenItemDefaults(ctx, workflowItemType)
	}()
	reads.Wait()

	// Track workflows by slug - more specific sources override less specific
	// Order: builtin (lowest) < project files < user (highest)
	workflowsBySlug := make(map[string]*reliantv1.WorkflowListItem)
	var invalidWorkflows []*reliantv1.InvalidWorkflow

	// 1. Load builtin workflows first (lowest priority)
	builtinEntries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read builtin workflows"))
	}
	for _, entry := range builtinEntries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}

		filename := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))

		data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
		if err != nil {
			logging.WarnContext(ctx, "Failed to read builtin workflow", "error", err, "file", entry.Name())
			invalidWorkflows = append(invalidWorkflows, &reliantv1.InvalidWorkflow{
				Name:   "builtin://" + filename,
				Source: "builtin",
				Path:   entry.Name(),
				Errors: []string{fmt.Sprintf("failed to read file: %v", err)},
			})
			continue
		}

		protoWf, err := parseBuiltinWorkflowFile(entry.Name(), data)
		if err != nil {
			logging.ErrorContext(ctx, "Failed to parse builtin workflow", "error", err, "file", entry.Name())
			invalidWorkflows = append(invalidWorkflows, &reliantv1.InvalidWorkflow{
				Name:   "builtin://" + filename,
				Source: "builtin",
				Path:   entry.Name(),
				Errors: []string{err.Error()},
			})
			continue
		}

		// Skip internal workflows (they're not user-selectable)
		if builtin.IsInternalWorkflow(protoWf.Name) {
			continue
		}

		// Hidden builtins are building blocks other workflows ref:; they stay
		// runnable and resolvable, just out of the default listing.
		if protoWf.Hidden && !req.Msg.GetIncludeHidden() {
			continue
		}

		workflowsBySlug["builtin://"+protoWf.Name] = &reliantv1.WorkflowListItem{
			Name:            "builtin://" + protoWf.Name,
			Title:           protoWf.Title,
			IsHidden:        protoWf.Hidden,
			Filename:        filename,
			Description:     protoWf.Description,
			StepCount:       int32(len(protoWf.Nodes)),
			Source:          "builtin",
			Nodes:           protoWf.Nodes,
			Edges:           protoWf.Edges,
			Inputs:          protoWf.Inputs,
			HasPresetGroups: rpcWorkflowHasPresetGroups(protoWf),
			Status:          reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE,
			AutomationOnly:  protoWf.AutomationOnly,
			Triggers:        protoWf.Triggers,
		}
	}

	// 2. Load project workflows from .reliant/workflows/*.yaml files (read-only, team-shared)
	projectWorkflows, projectInvalid := discoverProjectWorkflowsFromDB(catalog, ctx, project.ID)
	for _, pwf := range projectWorkflows {
		// Filename is the index's runnable address, not the YAML name:
		// colliding nested workflows have distinct repo-qualified addresses.
		workflowsBySlug[pwf.Filename] = pwf
	}
	invalidWorkflows = append(invalidWorkflows, projectInvalid...)

	// 3. Load user's workflows from database (user-owned, available across all projects).
	// Default listing is chat-safe: only workflows a run would accept — status
	// complete, visible, and passing validation NOW (validity is never stored;
	// a complete workflow can fall behind a stricter validator). Management UIs
	// such as the Workflow Hub opt into every workflow, drafts included, with
	// include_hidden=true; each carries its status and current findings.
	drafts, draftsErr := catalog.Drafts()
	if draftsErr != nil {
		logging.ErrorContext(ctx, "Failed to list workflows from database", "error", draftsErr, "user_id", userID, "include_hidden", req.Msg.GetIncludeHidden())
		// Continue with builtins and project workflows
	} else {
		// One loader for every workflow validated here, so a ref several of
		// them share (builtin://agent, a helper of the user's) resolves once.
		// Its rule is validateWorkflowDefinition's: DraftLoader's, with each
		// workflow resolving to itself.
		validationRefs := workflowcatalog.MemoizeRefLoader(workflowcatalog.WithBuiltinCache(workflowsource.DraftLoader(ctx, catalog, userID, nil)))
		validationLoader := func(self *reliantv1.Workflow) v2.WorkflowLoader {
			return resolvingToSelf(self, validationRefs)
		}
		for _, draft := range drafts {
			if !req.Msg.GetIncludeHidden() && (draft.IsHidden || draft.Status != db.WorkflowDraftStatusComplete) {
				continue
			}
			check := validateWorkflowDefinitionWith([]byte(draft.Definition), validationLoader)
			if !req.Msg.GetIncludeHidden() && !check.valid() {
				continue
			}
			item, err := userWorkflowListItem(draft, check)
			if err != nil {
				logging.ErrorContext(ctx, "Failed to parse stored workflow", "error", err, "id", draft.ID, "name", draft.Name)
				continue
			}
			// A definition that declares `hidden: true` is a building block, as
			// a hidden builtin is: out of the default listing, still resolvable.
			if item.IsHidden && !req.Msg.GetIncludeHidden() {
				continue
			}
			// Use draft.Slug directly as the map key - this is the stable runtime identifier.
			workflowsBySlug[draft.Slug] = item
		}
	}

	// Visibility state for builtin workflows (read above, 2 queries instead of 2×N)
	if overridesErr != nil {
		logging.WarnContext(ctx, "Failed to batch-load visibility overrides for workflows", "error", overridesErr)
		wfOverrides = nil
	}
	if hiddenErr != nil {
		logging.WarnContext(ctx, "Failed to batch-load hidden defaults for workflows", "error", hiddenErr)
		wfHiddenDefaults = nil
	}
	wfHiddenDefaultSet := make(map[string]bool, len(wfHiddenDefaults))
	for _, slug := range wfHiddenDefaults {
		wfHiddenDefaultSet[slug] = true
	}

	// What keeps each workflow from running with no machine, at its declared
	// defaults: the Automations form offers "No machine" only when this is
	// empty. Refs resolve by the rule a run uses (workflowsource), against
	// the catalog read above, and each distinct ref once.
	refOpts := workflowsource.Options{UserID: userID, ProjectID: project.ID}
	refLoader := workflowcatalog.MemoizeRefLoader(workflowcatalog.WithBuiltinCache(func(ref string) (*reliantv1.Workflow, error) {
		resolved, err := workflowsource.Resolve(ctx, catalog, refOpts, ref)
		if err != nil {
			return nil, err
		}
		return resolved.Workflow, nil
	}))
	preflight := memoizePreflight(tools.PreflightConfig())
	for _, wf := range workflowsBySlug {
		def := &reliantv1.Workflow{Name: wf.Name, Nodes: wf.Nodes, Edges: wf.Edges, Inputs: wf.Inputs}
		req := v2.MachineRequirements(def, nil, refLoader, preflight)
		wf.NeedsMachine = append(append([]string(nil), req.Hard...), req.Tools...)
	}

	// Filter by visibility and convert map to slice
	workflows := make([]*reliantv1.WorkflowListItem, 0, len(workflowsBySlug))
	for _, wf := range workflowsBySlug {
		// A project workflow whose definition declares `hidden: true` is a
		// building block: out of the default listing, like a hidden builtin.
		if wf.Source == "project" && wf.IsHidden && !req.Msg.GetIncludeHidden() {
			continue
		}
		// Check visibility for builtin workflows (unless include_hidden is set)
		if wf.Source == "builtin" && !req.Msg.GetIncludeHidden() {
			visible, ok := wfOverrides[wf.Name]
			if !ok {
				visible = !wfHiddenDefaultSet[wf.Name]
			}
			if !visible {
				continue
			}
		}
		workflows = append(workflows, wf)
	}

	return connect.NewResponse(&reliantv1.ListWorkflowsResponse{
		Workflows:        workflows,
		Total:            int32(len(workflows)),
		InvalidWorkflows: invalidWorkflows,
	}), nil
}

// userWorkflowListItem renders a stored workflow for list responses, with its
// lifecycle status and the validation findings computed for it now.
func userWorkflowListItem(draft *db.WorkflowDraft, check workflowCheck) (*reliantv1.WorkflowListItem, error) {
	protoWf, err := parseWorkflowYAML([]byte(draft.Definition))
	if err != nil {
		return nil, err
	}
	draftID := draft.ID // Copy for pointer
	item := &reliantv1.WorkflowListItem{
		Name:             draft.Name,
		Title:            protoWf.Title,
		Filename:         draft.Slug,
		Description:      protoWf.Description,
		StepCount:        int32(len(protoWf.Nodes)),
		Source:           "user",
		Nodes:            protoWf.Nodes,
		Edges:            protoWf.Edges,
		Inputs:           protoWf.Inputs,
		IsHidden:         draft.IsHidden || protoWf.Hidden,
		HasPresetGroups:  rpcWorkflowHasPresetGroups(protoWf),
		DraftId:          &draftID,
		Status:           draftStatusToProto(draft.Status),
		ValidationErrors: check.protoErrors(true),
		AutomationOnly:   protoWf.AutomationOnly,
		Triggers:         protoWf.Triggers,
	}
	if !draft.UpdatedAt.IsZero() {
		updatedAt := draft.UpdatedAt.Format(time.RFC3339)
		item.UpdatedAt = &updatedAt
	}
	return item, nil
}

// storedWorkflowParseWarn admits one "Failed to parse stored project
// workflow" line per file version per hour. Every ListWorkflows re-parses the
// project's synced files, so a project carrying files this server cannot
// parse (22 legacy files in one project, on 2026-10-28) logged each of them
// on every list, while the same finding was already in every response.
var storedWorkflowParseWarn = logging.NewThrottle(time.Hour, time.Now)

// storedWorkflowParseKey keys the throttle by the file's content as well as
// its path, so an edit that leaves it broken in a new way logs again.
func storedWorkflowParseKey(projectID, path string, content []byte) string {
	sum := sha256.Sum256(content)
	return projectID + "\x00" + path + "\x00" + hex.EncodeToString(sum[:8])
}

// discoverProjectWorkflowsFromDB loads project workflows from the stored config record (synced by daemon).
// Returns both valid workflows and invalid workflows that failed to parse.
func discoverProjectWorkflowsFromDB(repo workflowsource.ProjectStore, ctx context.Context, projectID string) ([]*reliantv1.WorkflowListItem, []*reliantv1.InvalidWorkflow) {
	// The project's workflows as the CLI indexes them on disk
	// (workflowref): a file no ref can address — no name:, a name another
	// file also declares — is listed as invalid, with the reason, rather
	// than one of them silently winning.
	index, err := workflowsource.ProjectIndex(ctx, repo, projectID)
	if err != nil {
		logging.WarnContext(ctx, "Failed to read stored project workflows", "error", err, "project_id", projectID)
		return nil, nil
	}

	var items []*reliantv1.WorkflowListItem
	var invalidWorkflows []*reliantv1.InvalidWorkflow

	for _, entry := range index.Entries() {
		label := entry.Name
		if label == "" {
			label = entry.Location()
		}
		if entry.Problem != nil {
			invalidWorkflows = append(invalidWorkflows, &reliantv1.InvalidWorkflow{
				Name:   label,
				Source: "project",
				Path:   entry.Location(),
				Errors: []string{entry.Problem.Error()},
			})
			continue
		}
		protoWf, err := parseWorkflowYAML(entry.Content)
		if err != nil {
			// The finding goes back to the caller as an invalid workflow on
			// every list; the log says it once per version of the file.
			if suppressed, ok := storedWorkflowParseWarn.Allow(storedWorkflowParseKey(projectID, entry.Location(), entry.Content)); ok {
				logging.WarnContext(ctx, "Failed to parse stored project workflow", "error", err, "path", entry.Location(),
					"project_id", projectID, "suppressed_since_last", suppressed)
			}
			invalidWorkflows = append(invalidWorkflows, &reliantv1.InvalidWorkflow{
				Name:   entry.Address,
				Source: "project",
				Path:   entry.Location(),
				Errors: []string{err.Error()},
			})
			continue
		}

		var addressWarnings []*reliantv1.ValidationError
		if entry.Warning != nil {
			addressWarnings = append(addressWarnings, &reliantv1.ValidationError{Type: "warning:discovery", Message: entry.Warning.Error()})
		}
		listedName := protoWf.Name
		if entry.Address != entry.Slug {
			listedName = entry.Address
		}
		items = append(items, &reliantv1.WorkflowListItem{
			ValidationErrors: addressWarnings,
			Name:             listedName,
			Title:            protoWf.Title,
			IsHidden:         protoWf.Hidden,
			// The address a ref resolves to: a nested repo's workflow whose
			// bare name the root or another repo also claims is
			// "<repo>/<slug>" (workflowref.Index).
			Filename:        entry.Address,
			Description:     protoWf.Description,
			StepCount:       int32(len(protoWf.Nodes)),
			Source:          "project",
			Nodes:           protoWf.Nodes,
			Edges:           protoWf.Edges,
			Inputs:          protoWf.Inputs,
			HasPresetGroups: rpcWorkflowHasPresetGroups(protoWf),
			Status:          reliantv1.WorkflowDraftStatus_WORKFLOW_DRAFT_STATUS_COMPLETE,
			AutomationOnly:  protoWf.AutomationOnly,
			Triggers:        protoWf.Triggers,
		})
	}

	return items, invalidWorkflows
}
