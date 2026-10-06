// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// PreflightDaemonCheckInput is the input for the preflight daemon check activity.
type PreflightDaemonCheckInput struct {
	ChatID         string                   `json:"chat_id" reliant:"-"`
	DaemonSelector *toolexec.DaemonSelector `json:"daemon_selector,omitempty"`
}

// PreflightDaemonCheckOutput is the output of the preflight daemon check.
type PreflightDaemonCheckOutput struct {
	DaemonAvailable bool   `json:"daemon_available"`
	DaemonID        string `json:"daemon_id,omitempty"`
}

// PreflightDaemonCheckActivity checks if a daemon is available before workflow execution.
// This fails fast with a clear error message if a daemon is required but not available.
type PreflightDaemonCheckActivity struct {
	repo         db.Repository
	toolExecutor toolexec.ToolExecutor
}

// NewPreflightDaemonCheckActivity creates a new PreflightDaemonCheckActivity.
func NewPreflightDaemonCheckActivity(repo db.Repository, toolExecutor toolexec.ToolExecutor) *PreflightDaemonCheckActivity {
	return &PreflightDaemonCheckActivity{
		repo:         repo,
		toolExecutor: toolExecutor,
	}
}

func (a *PreflightDaemonCheckActivity) Name() string {
	return "PreflightDaemonCheck"
}

func (a *PreflightDaemonCheckActivity) DisplayName() string {
	return "Preflight Daemon Check"
}

func (a *PreflightDaemonCheckActivity) Description() string {
	return "Check if a daemon is available for tool execution"
}

func (a *PreflightDaemonCheckActivity) Category() schema.ActivityCategory {
	return schema.CategoryUtility
}

func (a *PreflightDaemonCheckActivity) Execute(ctx context.Context, input PreflightDaemonCheckInput) (PreflightDaemonCheckOutput, error) {
	// Resolve the user ID from the chat
	chat, err := a.repo.GetChat(ctx, input.ChatID)
	if err != nil {
		return PreflightDaemonCheckOutput{}, fmt.Errorf("failed to get chat for preflight check: %w", err)
	}

	// A run with no machine never wakes one. Its launch already refused a
	// workflow that hard-requires a machine, and the tools it is offered all
	// run without one, so there is nothing for this check to establish — and
	// waking here is exactly what must not happen.
	if chat.NoMachine {
		return PreflightDaemonCheckOutput{DaemonAvailable: false}, nil
	}

	project, err := a.repo.GetProject(ctx, chat.ProjectID)
	if err != nil {
		return PreflightDaemonCheckOutput{}, fmt.Errorf("failed to get project for preflight check: %w", err)
	}

	// Use the RemoteExecutor's daemon router to check daemon availability.
	// We access it through the ToolExecutor interface.
	remoteExec, ok := a.toolExecutor.(*toolexec.RemoteExecutor)
	if !ok {
		// With a non-remote executor, daemon is always available.
		return PreflightDaemonCheckOutput{DaemonAvailable: true}, nil
	}

	router := remoteExec.DaemonRouter()
	if router == nil {
		return PreflightDaemonCheckOutput{}, fmt.Errorf("daemon router not configured")
	}

	// Only an unattended run — one a stored trigger launched, of any kind — may
	// wake its daemon with the delegated automation token; an attended run
	// (chat.start, agent start_run, builder test) acts as the signed-in user or
	// not at all. The kind is the chat's launch event, the record of what
	// started it.
	if ev, evErr := a.repo.GetTriggerEventByChatID(ctx, input.ChatID); evErr == nil && ev != nil &&
		ev.Kind.Unattended() {
		ctx = automationcred.Allow(ctx)
	}

	// Check if daemon is online
	online, err := router.IsDaemonOnline(ctx, project.UserID)
	if err != nil {
		// Infrastructure error — don't fail, let it proceed and fail at tool execution.
		return PreflightDaemonCheckOutput{DaemonAvailable: true}, nil
	}

	if !online {
		// Preflight is the run's wake point: the only place, besides an
		// attended send, that may resume a suspended daemon.
		if waker, ok := router.(toolexec.DaemonWaker); ok {
			if daemonID, wakeErr := waker.EnsureAwake(ctx, project.UserID, input.DaemonSelector); wakeErr == nil {
				return PreflightDaemonCheckOutput{DaemonAvailable: true, DaemonID: daemonID}, nil
			}
		}

		return PreflightDaemonCheckOutput{DaemonAvailable: false},
			fmt.Errorf("this workflow requires a daemon but none is available. Start one locally with 'reliant daemon start' or deploy a cloud daemon")
	}

	return PreflightDaemonCheckOutput{DaemonAvailable: true}, nil
}
