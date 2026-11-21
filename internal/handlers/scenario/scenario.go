// Copyright (c) 2025 Reliant Labs
package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/ptr"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
)

// projectScenariosReader reads the synced project scenarios column of a
// project's config record, and nothing else from it
// (db.Repo.GetProjectScenariosJSON). It returns svcerr.NotFound when the
// project has no record.
type projectScenariosReader interface {
	GetProjectScenariosJSON(ctx context.Context, projectID string) (*string, error)
}

// The repository every server wires in must answer it, checked at build time.
var _ projectScenariosReader = (*db.Repo)(nil)

// storedProjectScenarios returns the project's synced scenarios. A project
// with no config record returns svcerr.NotFound.
//
// It reads the one column the scenario RPCs need. The whole config record
// carries every skill body and repo memory the daemon indexed — 18 MB in
// prod — for a column that is empty or a few KB.
func storedProjectScenarios(ctx context.Context, repo Repository, projectID string) ([]cfg.StoredScenario, error) {
	// The method lives on *db.Repo, not on Repository; every production
	// wiring passes a *db.Repo. A repository that cannot answer is a wiring
	// fault, reported rather than papered over with the full-record read.
	reader, ok := repo.(projectScenariosReader)
	if !ok {
		return nil, fmt.Errorf("stored project scenarios: %T cannot read the scenarios column (GetProjectScenariosJSON)", repo)
	}
	scenariosJSON, err := reader.GetProjectScenariosJSON(ctx, projectID)
	if err != nil {
		return nil, err
	}
	scenarios, err := cfg.ParseStoredScenarios(scenariosJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to parse stored scenarios: %w", err)
	}
	return scenarios, nil
}

// projectBelongsToUser verifies the authenticated user owns the given project.
func (s *Service) projectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	_, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID)
	return svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
}

// ============================================================================
// Conversion Helpers
// ============================================================================

func scenarioToProto(s *db.WorkflowScenario) (*reliantv1.Scenario, error) {
	if s == nil {
		return nil, nil
	}

	proto := &reliantv1.Scenario{
		Id:        s.ID,
		UserId:    s.UserID,
		Name:      s.Name,
		CreatedAt: s.CreatedAt.Format(time.RFC3339),
		UpdatedAt: s.UpdatedAt.Format(time.RFC3339),
	}

	proto.WorkflowDraftId = ptr.From(s.WorkflowDraftID)
	proto.Description = ptr.From(s.Description)
	proto.LastRunStatus = ptr.From(s.LastRunStatus)

	// Parse the full scenario YAML and convert to proto
	var scenario wfscenario.Scenario
	if err := yaml.Unmarshal([]byte(s.Events), &scenario); err != nil {
		return nil, fmt.Errorf("failed to unmarshal scenario events: %w", err)
	}
	events, err := scenarioEventsToProto(scenario.Events)
	if err != nil {
		return nil, err
	}
	proto.Events = events
	if scenario.Expect != nil {
		proto.Expect = scenarioExpectToProto(scenario.Expect)
	}

	// Parse last run result from JSON
	if s.LastRunResult != nil {
		var result reliantv1.ScenarioResult
		if err := json.Unmarshal([]byte(*s.LastRunResult), &result); err != nil {
			return nil, fmt.Errorf("failed to unmarshal last run result: %w", err)
		}
		proto.LastRunResult = &result
	}

	return proto, nil
}

func protoToScenario(p *reliantv1.ScenarioDefinition) *wfscenario.Scenario {
	if p == nil {
		return nil
	}

	s := &wfscenario.Scenario{
		Name:        p.Name,
		Description: p.Description,
		Events:      make([]wfscenario.SimulatedEvent, len(p.Events)),
	}

	// Parse inputs from JSON
	if p.InputsJson != "" {
		var inputs map[string]interface{}
		if err := json.Unmarshal([]byte(p.InputsJson), &inputs); err == nil {
			s.Inputs = inputs
		}
	}

	// Convert events
	for i, e := range p.Events {
		event := wfscenario.SimulatedEvent{
			Node: e.Node,
		}

		// Parse output from JSON
		if e.OutputJson != "" {
			var output map[string]interface{}
			if err := json.Unmarshal([]byte(e.OutputJson), &output); err == nil {
				event.Output = output
			}
		}

		s.Events[i] = event
	}

	// Convert expectation
	if p.Expect != nil {
		s.Expect = &wfscenario.Expectation{
			Outcome:       wfscenario.ExpectedOutcome(p.Expect.Outcome),
			Reached:       p.Expect.Reached,
			NotReached:    p.Expect.NotReached,
			ErrorContains: p.Expect.ErrorContains,
			ErrorNode:     p.Expect.ErrorNode,
		}
	}

	return s
}

// dbScenarioToScenario reads a stored scenario through the same strict parser
// the project scenario files use. An unparseable one is an error: running an
// empty stand-in instead would pass without testing anything.
func dbScenarioToScenario(s *db.WorkflowScenario) (*wfscenario.Scenario, error) {
	if s == nil {
		return nil, nil
	}
	sc, err := wfscenario.ParseScenarioFile([]byte(s.Events), s.Name)
	if err != nil {
		return nil, fmt.Errorf("scenario %s: %w", s.Name, err)
	}
	return sc, nil
}

func scenarioEventsToProto(events []wfscenario.SimulatedEvent) ([]*reliantv1.SimulatedEvent, error) {
	var protoEvents []*reliantv1.SimulatedEvent
	for _, e := range events {
		pe := &reliantv1.SimulatedEvent{
			Node: e.Node,
		}
		if e.Output != nil {
			data, err := json.Marshal(e.Output)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal event output: %w", err)
			}
			pe.OutputJson = string(data)
		}
		protoEvents = append(protoEvents, pe)
	}
	return protoEvents, nil
}

func scenarioExpectToProto(e *wfscenario.Expectation) *reliantv1.ScenarioExpectation {
	if e == nil {
		return nil
	}
	return &reliantv1.ScenarioExpectation{
		Outcome:       string(e.Outcome),
		Reached:       e.Reached,
		NotReached:    e.NotReached,
		ErrorContains: e.ErrorContains,
		ErrorNode:     e.ErrorNode,
	}
}

func scenarioResultToProto(r *wfscenario.ScenarioResult) *reliantv1.ScenarioResult {
	if r == nil {
		return nil
	}

	proto := &reliantv1.ScenarioResult{
		Status:       string(r.Status),
		ScenarioName: r.Scenario,
		Mismatches:   r.Mismatches,
	}

	// Convert execution details
	proto.Execution = &reliantv1.ExecutionDetails{
		NodesReached: r.Execution.NodesReached,
		Outcome:      r.Execution.Outcome,
		DurationMs:   r.Execution.DurationMs,
	}

	if r.Execution.Error != nil {
		proto.Execution.Error = &reliantv1.ErrorDetails{
			Node:       r.Execution.Error.Node,
			Step:       r.Execution.Error.Step,
			Message:    r.Execution.Error.Message,
			Expression: r.Execution.Error.Expression,
		}
	}

	// Convert expected
	if r.Expected != nil {
		proto.Expected = &reliantv1.ScenarioExpectation{
			Outcome:       string(r.Expected.Outcome),
			Reached:       r.Expected.Reached,
			NotReached:    r.Expected.NotReached,
			ErrorContains: r.Expected.ErrorContains,
			ErrorNode:     r.Expected.ErrorNode,
		}
	}

	return proto
}

// scenarioToProtoScenario converts a wfscenario.Scenario to a proto Scenario
func scenarioToProtoScenario(s *wfscenario.Scenario, id, filePath string) (*reliantv1.Scenario, error) {
	if s == nil {
		return nil, nil
	}

	events, err := scenarioEventsToProto(s.Events)
	if err != nil {
		return nil, err
	}

	return &reliantv1.Scenario{
		Id:          id,
		Name:        s.Name,
		Description: s.Description,
		Source:      "project",
		Path:        filePath,
		Events:      events,
		Expect:      scenarioExpectToProto(s.Expect),
	}, nil
}
