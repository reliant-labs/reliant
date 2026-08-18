package services

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func createScenarioWorkflowLoader(repo db.Repository, ctx context.Context, userID, projectID string) func(string) (*reliantv1.Workflow, error) {
	return func(ref string) (*reliantv1.Workflow, error) {
		workflowRef := strings.TrimSpace(ref)
		if workflowRef == "" {
			return nil, fmt.Errorf("workflow ref is empty")
		}

		if strings.HasPrefix(workflowRef, "builtin://") {
			return loadScenarioBuiltinWorkflow(workflowRef)
		}

		workflowRef = strings.TrimPrefix(workflowRef, "project://")

		if userID != "" {
			slug := cfg.NormalizeSlug(workflowRef)
			if slug != "" {
				draft, err := repo.GetUsableWorkflowBySlug(ctx, userID, slug)
				if err != nil {
					return nil, fmt.Errorf("failed to look up workflow %q: %w", ref, err)
				}
				if draft != nil {
					wf, err := wfyaml.ParseWorkflow([]byte(draft.Definition))
					if err != nil {
						return nil, fmt.Errorf("failed to parse workflow %q: %w", ref, err)
					}
					return wf, nil
				}
			}
		}

		if projectID != "" {
			wf, err := loadScenarioProjectWorkflow(repo, ctx, projectID, workflowRef)
			if err != nil {
				return nil, err
			}
			if wf != nil {
				return wf, nil
			}
		}

		return nil, fmt.Errorf("workflow not found: %s", ref)
	}
}

func loadScenarioBuiltinWorkflow(ref string) (*reliantv1.Workflow, error) {
	name := strings.TrimPrefix(ref, "builtin://")
	if yamlData := builtin.GetInternalWorkflowYAML(name); yamlData != nil {
		wf, err := wfyaml.ParseWorkflow(yamlData)
		if err != nil {
			return nil, fmt.Errorf("failed to parse internal workflow %q: %w", ref, err)
		}
		return wf, nil
	}

	data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("builtin workflow not found: %s", ref)
	}
	wf, err := wfyaml.ParseWorkflow(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse builtin workflow %q: %w", ref, err)
	}
	return wf, nil
}

func loadScenarioProjectWorkflow(repo db.Repository, ctx context.Context, projectID, workflowRef string) (*reliantv1.Workflow, error) {
	record, err := repo.GetProjectConfigRecord(ctx, projectID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to load project config for workflow %q: %w", workflowRef, err)
	}

	workflows, err := cfg.ParseStoredWorkflows(record.ProjectWorkflowsJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to parse stored project workflows: %w", err)
	}

	slug := cfg.NormalizeSlug(workflowRef)
	storedWorkflow := cfg.FindStoredWorkflowBySlug(workflows, slug)
	if storedWorkflow == nil {
		return nil, nil
	}

	wf, err := wfyaml.ParseWorkflow([]byte(storedWorkflow.YAMLContent))
	if err != nil {
		return nil, fmt.Errorf("failed to parse project workflow %q: %w", workflowRef, err)
	}
	return wf, nil
}
