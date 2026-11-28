// Copyright (c) 2026 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/chatviews"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinepreflight"
	"github.com/reliant-labs/reliant/internal/toolexec"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// MachinePreflightInput is the node preflight's input: the machine to check
// and what to ask of it. The runtime's MachinePreflightInput is its twin.
type MachinePreflightInput struct {
	ChatID         string                        `json:"chat_id" reliant:"-"`
	WorkflowID     string                        `json:"workflow_id" reliant:"-"`
	NodePath       string                        `json:"node_path,omitempty"`
	DaemonSelector *activitytypes.DaemonSelector `json:"daemon_selector,omitempty"`
	Pool           string                        `json:"pool,omitempty"`
	Spec           machinepreflight.Spec         `json:"spec"`
}

// MachinePreflightOutput is the verdict. Zero is a machine that passed.
type MachinePreflightOutput struct {
	Failed     bool   `json:"failed,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
	DaemonID   string `json:"daemon_id,omitempty"`
	DaemonName string `json:"daemon_name,omitempty"`
}

// machinePreflightCommandTimeout bounds the daemon round trip: the check is
// a handful of stat calls and a PATH lookup per tool.
const machinePreflightCommandTimeout = 30 * time.Second

// MachinePreflightActivity is a node's machine self-check (Node.preflight):
// it sends machinepreflight.Command to the machine the node routes to and
// returns the verdict. A machine that fails is a verdict, never an error, so
// the workflow can move the node to another machine without spending one of
// its attempts (internal/workflow/runtime node_preflight.go). An error is a
// check that could not run (a database read, a transport failure), which the
// activity's retry policy owns.
//
// Every failure is a machine-health signal: logged at WARN with the machine,
// the node and the reason, and counted on reliant.machine.preflight.failures
// by reason.
type MachinePreflightActivity struct {
	repo     PreflightDaemonCheckStore
	launches LaunchEvents
	rows     WorkspaceRows
	router   toolexec.DaemonRouter
}

// NewMachinePreflightActivity builds the activity. router may be nil (a worker
// with no daemons), and every machine then passes: there is none to check.
func NewMachinePreflightActivity(repo PreflightDaemonCheckStore, launches LaunchEvents, rows WorkspaceRows, router toolexec.DaemonRouter) *MachinePreflightActivity {
	return &MachinePreflightActivity{repo: repo, launches: launches, rows: rows, router: router}
}

func (a *MachinePreflightActivity) Name() string        { return "MachinePreflight" }
func (a *MachinePreflightActivity) DisplayName() string { return "Machine Preflight" }
func (a *MachinePreflightActivity) Description() string {
	return "Check the machine a node runs on (disk, /tmp, uid, tools, load) before the node's work starts"
}
func (a *MachinePreflightActivity) Category() schema.ActivityCategory {
	return schema.CategoryUtility
}

func (a *MachinePreflightActivity) Execute(ctx context.Context, input MachinePreflightInput) (MachinePreflightOutput, error) {
	if a.router == nil {
		return MachinePreflightOutput{}, nil
	}
	chat, err := chatviews.Of(a.repo).GetChat(ctx, input.ChatID)
	if err != nil {
		return MachinePreflightOutput{}, fmt.Errorf("read the chat for a machine preflight: %w", err)
	}
	if chat.NoMachine {
		// A run with no machine has none to check; its nodes that need one
		// fail on their own terms.
		return MachinePreflightOutput{}, nil
	}
	project, err := a.rows.GetProject(ctx, chat.ProjectID)
	if err != nil {
		return MachinePreflightOutput{}, fmt.Errorf("read the project for a machine preflight: %w", err)
	}
	if a.launches == nil {
		return MachinePreflightOutput{}, fmt.Errorf("machine preflight: launch events are not configured")
	}
	if ev, evErr := a.launches.GetTriggerEventByChatID(ctx, input.ChatID); evErr == nil && ev != nil && ev.Kind.Unattended() {
		ctx = automationcred.Allow(ctx)
	}
	selector := runMachineSelector(ctx, a.rows, chat, input.DaemonSelector)

	out := MachinePreflightOutput{}
	if selector != nil {
		out.DaemonID = selector.ID
	}
	daemonID, err := toolexec.ResolveDaemonIDForSelector(ctx, a.router, project.UserID, selector)
	if err != nil {
		if daemonerr.Unreachable(err) {
			return a.failed(ctx, input, out, machinepreflight.ReasonUnreachable, "the machine is not connected"), nil
		}
		return MachinePreflightOutput{}, fmt.Errorf("resolve the machine to check: %w", err)
	}
	out.DaemonID = daemonID
	if d, err := a.rows.GetDaemon(ctx, daemonID); err == nil && d != nil {
		out.DaemonName = d.Name
	}

	payload, err := json.Marshal(machinepreflight.Request{Spec: input.Spec})
	if err != nil {
		return MachinePreflightOutput{}, fmt.Errorf("encode the machine preflight: %w", err)
	}
	resp, err := a.router.SendDaemonCommandToDaemon(ctx, project.UserID, daemonID, machinepreflight.Command, payload, int32(machinePreflightCommandTimeout/time.Millisecond))
	switch {
	case err != nil && daemonerr.IsOutdated(err):
		return a.failed(ctx, input, out, machinepreflight.ReasonDaemonOutdated,
			"the machine's Reliant predates the preflight check; update it to run nodes that set preflight"), nil
	case err != nil && daemonerr.Unreachable(err):
		return a.failed(ctx, input, out, machinepreflight.ReasonUnreachable, "the machine is not connected"), nil
	case err != nil:
		return MachinePreflightOutput{}, fmt.Errorf("run the machine preflight: %w", err)
	}
	var res machinepreflight.Result
	if err := json.Unmarshal(resp, &res); err != nil {
		return a.failed(ctx, input, out, machinepreflight.ReasonInvalid, "the machine's preflight answer could not be read"), nil
	}
	if res.OK {
		return out, nil
	}
	return a.failed(ctx, input, out, res.Primary().Reason, res.Message()), nil
}

// failed records a failed check — the log line and the metric — and returns
// its verdict.
func (a *MachinePreflightActivity) failed(ctx context.Context, input MachinePreflightInput, out MachinePreflightOutput, reason machinepreflight.Reason, message string) MachinePreflightOutput {
	out.Failed, out.Reason, out.Message = true, string(reason), message
	logging.WarnContext(ctx, "[MachinePreflight] A node's machine failed its preflight",
		"chatID", input.ChatID, "workflowID", input.WorkflowID, "nodePath", input.NodePath,
		"pool", input.Pool, "daemonID", out.DaemonID, "daemonName", out.DaemonName,
		"reason", out.Reason, "message", out.Message)
	preflightFailures().Add(ctx, 1, metric.WithAttributes(attribute.String("reason", out.Reason)))
	return out
}

var (
	preflightFailuresOnce sync.Once
	preflightFailuresCtr  metric.Int64Counter
)

// preflightFailures is the reliant.machine.preflight.failures counter, built
// on first use from the process's meter provider.
func preflightFailures() metric.Int64Counter {
	preflightFailuresOnce.Do(func() {
		var err error
		preflightFailuresCtr, err = otel.GetMeterProvider().Meter("github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers").
			Int64Counter("reliant.machine.preflight.failures",
				metric.WithUnit("{check}"),
				metric.WithDescription("Node preflights a machine failed, by reason (machinepreflight.Reason). Each one moved the node to another machine, or failed it with no healthy machine."))
		// An instrument the SDK refuses still comes back usable (a no-op).
		otel.Handle(err)
	})
	return preflightFailuresCtr
}
