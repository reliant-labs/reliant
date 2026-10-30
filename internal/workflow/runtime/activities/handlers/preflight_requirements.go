// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/requirements"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"go.temporal.io/sdk/temporal"
)

// PreflightRequirementsInput is the run's declared `requires:` and who the run
// is, so the checks see the machine and the connections its nodes will.
type PreflightRequirementsInput struct {
	ChatID         string                   `json:"chat_id" reliant:"-"`
	WorkflowID     string                   `json:"workflow_id" reliant:"-"`
	DaemonSelector *toolexec.DaemonSelector `json:"daemon_selector,omitempty"`
	Tools          []string                 `json:"tools,omitempty"`
	Integrations   []string                 `json:"integrations,omitempty"`
	Repos          []string                 `json:"repos,omitempty"`
	ActingIdentity string                   `json:"acting_identity,omitempty"`
	Automation     string                   `json:"automation,omitempty"`
}

// PreflightRequirementsOutput lists what is missing; empty when everything
// is there.
type PreflightRequirementsOutput struct {
	Problems []requirements.Problem `json:"problems,omitempty"`
}

// PreflightRequirementsErrType is the non-retryable error a run whose
// requirements are not met fails with. Its message names every problem.
const PreflightRequirementsErrType = requirements.UnmetErrType

// preflightProbeTimeout bounds the probe command on the machine (ls-remote of
// each repo included).
const preflightProbeTimeout = 3 * time.Minute

// PreflightRequirementsActivity checks a run's `requires:` before its first
// node: tools on the machine's PATH, checkouts that exist and reach origin
// with no local tag diverged from origin's, and integrations whose connection
// resolves for the run's owner. Unmet requirements fail the run, with every
// problem named; a check that could not run (the machine did not answer) is
// an ordinary error the activity's retry policy owns.
type PreflightRequirementsActivity struct {
	repo        RunExecutorContextStore
	executor    toolexec.ToolExecutor
	credentials httpaction.CredentialSource
	// machine overrides the daemon-backed probe runner; tests set it.
	machine func(RunExecutorContext) requirements.Machine
}

// NewPreflightRequirementsActivity builds the activity. credentials may be nil
// (a worker with no connections), which fails any required integration.
func NewPreflightRequirementsActivity(repo RunExecutorContextStore, executor toolexec.ToolExecutor, credentials httpaction.CredentialSource) *PreflightRequirementsActivity {
	return &PreflightRequirementsActivity{repo: repo, executor: executor, credentials: credentials}
}

func (a *PreflightRequirementsActivity) Name() string        { return "PreflightRequirements" }
func (a *PreflightRequirementsActivity) DisplayName() string { return "Preflight Requirements" }
func (a *PreflightRequirementsActivity) Description() string {
	return "Check a run's declared requirements (tools, integrations, checkouts) before its first node"
}
func (a *PreflightRequirementsActivity) Category() schema.ActivityCategory {
	return schema.CategoryUtility
}

func (a *PreflightRequirementsActivity) Execute(ctx context.Context, input PreflightRequirementsInput) (PreflightRequirementsOutput, error) {
	req := &reliantv1.WorkflowRequirements{Tools: input.Tools, Integrations: input.Integrations, Repos: input.Repos}
	if requirements.Empty(req) {
		return PreflightRequirementsOutput{}, nil
	}

	var machine requirements.Machine
	if requirements.NeedsMachine(req) {
		execCtx, err := resolveRunExecutorContext(ctx, a.repo, input.ChatID)
		if err != nil {
			return PreflightRequirementsOutput{}, fmt.Errorf("resolve the run's machine: %w", err)
		}
		if !execCtx.NoMachine {
			if input.DaemonSelector != nil {
				execCtx.DaemonSelector = input.DaemonSelector
			}
			// The probe runs where the run's commands run: the project root,
			// with the machine's own credentials (ls-remote authenticates).
			execCtx.WorktreeID, execCtx.WorktreePath = "", ""
			if a.machine != nil {
				machine = a.machine(execCtx)
			} else {
				remote := NewRemoteRunExecutor(a.executor)
				remote.SetContext(execCtx)
				machine = runExecutorMachine{exec: remote, dir: execCtx.ProjectPath}
			}
		}
	}

	var creds requirements.Credentials
	if a.credentials != nil {
		creds = connectionCheck{
			src: a.credentials,
			site: httpaction.CredentialRequest{
				RunID: input.WorkflowID, NodeID: "preflight",
				ActingIdentity: input.ActingIdentity, Automation: input.Automation,
			},
		}
	}

	report, err := requirements.Check(ctx, req, machine, creds)
	if err != nil {
		return PreflightRequirementsOutput{}, err
	}
	if !report.OK() {
		return PreflightRequirementsOutput{Problems: report.Problems},
			temporal.NewNonRetryableApplicationError(report.Error(), PreflightRequirementsErrType, nil, report.Problems)
	}
	return PreflightRequirementsOutput{}, nil
}

// runExecutorMachine runs the probe through the same path a run node's
// command takes.
type runExecutorMachine struct {
	exec RunExecutor
	dir  string
}

func (m runExecutorMachine) Run(ctx context.Context, command string) (string, int, error) {
	stdout, _, code, _, err := m.exec.ExecuteCommand(ctx, command, m.dir, int(preflightProbeTimeout/time.Millisecond), nil)
	return stdout, code, err
}

// connectionCheck resolves an integration's connection the way an action
// node of the run would, without using it. An integration that declares no
// auth needs none.
type connectionCheck struct {
	src  httpaction.CredentialSource
	site httpaction.CredentialRequest
}

func (c connectionCheck) Check(ctx context.Context, integration string) error {
	var manifest *reliantv1.IntegrationManifest
	for _, m := range catalog.MustBuiltin().Manifests() {
		if m.GetId() == integration {
			manifest = m
			break
		}
	}
	if manifest == nil {
		return fmt.Errorf("%s is not an integration this release knows", integration)
	}
	conn := manifest.GetConnection()
	if len(conn.GetAuth()) == 0 {
		return nil
	}
	req := c.site
	req.IntegrationID, req.Connection, req.ServerPlaced = integration, conn, true
	cred, err := c.src.Credential(ctx, req)
	if err != nil {
		var ce *httpaction.CredentialError
		if errors.As(err, &ce) && ce.Message != "" {
			return errors.New(strings.TrimRight(ce.Message, ". "))
		}
		return fmt.Errorf("its connection could not be resolved: %w", err)
	}
	if cred == nil {
		return errors.New("its connection resolved to no credential")
	}
	return nil
}
