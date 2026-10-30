// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// MachineResumeWakeInput is one slice of a resuming run's wake.
type MachineResumeWakeInput struct {
	ChatID string `json:"chat_id" reliant:"-"`
	// DaemonSelector is the run's selector; Session marks the chat's pin,
	// which yields to the chat's workspace owner as it does for tools.
	DaemonSelector *activitytypes.DaemonSelector `json:"daemon_selector,omitempty"`
	// ToolNames are the tool calls the step about to run makes, when it is an
	// execute_tools step. A batch none of whose tools touch the machine wakes
	// nothing. ToolsKnown false means the step needs the machine whatever it
	// is (a run node).
	ToolNames  []string `json:"tool_names,omitempty"`
	ToolsKnown bool     `json:"tools_known,omitempty"`
	// WaitSeconds is how long this slice polls for the machine to attach
	// after waking it.
	WaitSeconds int `json:"wait_seconds,omitempty"`
	// Final marks the workflow's last slice: the chat's waiting marker is
	// cleared even if the machine is still not up, because the step runs now.
	Final bool `json:"final,omitempty"`
}

// MachineResumeWakeOutput says how the slice ended.
type MachineResumeWakeOutput struct {
	// Ready: the machine is attached, or the step does not need it.
	Ready bool `json:"ready"`
	// Waiting: the machine is coming up and the slice ended first.
	Waiting bool `json:"waiting,omitempty"`
	// Woke: this slice asked the control plane to wake the machine.
	Woke bool `json:"woke,omitempty"`
	// NotComing: the machine will not attach by waiting (removed, failed to
	// start, or the wake was refused). The step runs anyway and reports it.
	NotComing string `json:"not_coming,omitempty"`
}

// MachineResumeWakeActivity wakes a run's machine before its first machine
// step after a hold, and waits for it to attach.
//
// The idle policy suspends a machine that saw no tool traffic for its idle
// timeout, which is exactly what a run parked on a provider usage limit, a
// timer, a sub-agent or a pause looks like. That is intended. But tool-time
// traffic never wakes a machine (#406), so a run that resumed straight into a
// tool call used to spin on "machine asleep" until a human woke it
// (machine-suspend-2026-10-20: ~6h). The workflow now runs this, like run
// preflight, at the resume point (internal/workflow/runtime/machine_resume_wake.go).
//
// It is infrastructure, not a tool: the model is never told, and a failure
// here never fails the run — the step runs and reports whatever the machine
// is doing, exactly as before.
type MachineResumeWakeActivity struct {
	repo         MachineResumeWakeStore
	toolExecutor toolexec.ToolExecutor
	// pollInterval is how often a slice re-checks the machine; zero means
	// preflightPollInterval. Tests shrink it.
	pollInterval time.Duration
}

// NewMachineResumeWakeActivity creates a MachineResumeWakeActivity.
func NewMachineResumeWakeActivity(repo MachineResumeWakeStore, toolExecutor toolexec.ToolExecutor) *MachineResumeWakeActivity {
	return &MachineResumeWakeActivity{repo: repo, toolExecutor: toolExecutor}
}

func (a *MachineResumeWakeActivity) Name() string { return "MachineResumeWake" }

func (a *MachineResumeWakeActivity) DisplayName() string { return "Wake Machine On Resume" }

func (a *MachineResumeWakeActivity) Description() string {
	return "Wake the run's machine when the run resumes after a hold"
}

func (a *MachineResumeWakeActivity) Category() schema.ActivityCategory {
	return schema.CategoryUtility
}

func (a *MachineResumeWakeActivity) Execute(ctx context.Context, input MachineResumeWakeInput) (MachineResumeWakeOutput, error) {
	if input.ToolsKnown && !anyNeedsMachine(input.ToolNames) {
		return MachineResumeWakeOutput{Ready: true}, nil
	}
	chat, err := a.repo.GetChat(ctx, input.ChatID)
	if err != nil {
		return MachineResumeWakeOutput{}, fmt.Errorf("loading chat for the resume wake: %w", err)
	}
	if chat.NoMachine {
		return MachineResumeWakeOutput{Ready: true}, nil
	}
	// A resumed run can still carry the snapshot from before its chat was
	// pinned, or a pin from before its chat moved to its workspace's machine.
	// Check/wake the same machine its commands and tools will use.
	selector := runMachineSelector(ctx, a.repo, chat, input.DaemonSelector)
	project, err := a.repo.GetProject(ctx, chat.ProjectID)
	if err != nil {
		return MachineResumeWakeOutput{}, fmt.Errorf("loading project for the resume wake: %w", err)
	}
	remoteExec, ok := a.toolExecutor.(*toolexec.RemoteExecutor)
	if !ok || remoteExec.DaemonRouter() == nil {
		return MachineResumeWakeOutput{Ready: true}, nil
	}
	router := remoteExec.DaemonRouter()
	userID := project.UserID
	exitCtx := context.WithoutCancel(ctx)

	if online, err := router.IsDaemonOnline(ctx, userID, selector); err != nil || online {
		// Up — or the check itself failed, which the step's own call
		// reports better than a guess here.
		return MachineResumeWakeOutput{Ready: true}, nil
	}

	out := MachineResumeWakeOutput{}
	if notComing := machineNotComing(ctx, router, userID, selector); notComing != nil {
		return MachineResumeWakeOutput{Ready: true, NotComing: notComing.Error()}, nil
	}
	if waker, ok := router.(toolexec.RunWaker); ok {
		// Only used if the control plane predates the run-resume door and the
		// wake falls back to a token: an unattended run's is its trigger's.
		if ev, evErr := a.repo.GetTriggerEventByChatID(ctx, input.ChatID); evErr == nil && ev != nil && ev.Kind.Unattended() {
			ctx = automationcred.Allow(ctx)
		}
		res, wakeErr := waker.WakeForRun(ctx, userID, selector)
		switch {
		case wakeErr == nil:
			out.Woke = res.Resumed
		case toolexec.IsDaemonPending(wakeErr):
			// Already on its way up.
		default:
			logging.WarnContext(ctx, "[MachineResumeWake] Could not wake the machine for the resuming run; the step will report it",
				"chatID", input.ChatID, "userID", userID, "error", wakeErr)
			return MachineResumeWakeOutput{Ready: true, NotComing: wakeErr.Error()}, nil
		}
	}

	// The machine is on its way: the chat says so (WAITING_FOR_DAEMON) while
	// the run waits for it.
	a.setBlocked(ctx, input.ChatID, true)
	deadline := time.NewTimer(time.Duration(input.WaitSeconds) * time.Second)
	defer deadline.Stop()
	poll := a.pollInterval
	if poll <= 0 {
		poll = preflightPollInterval
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.setBlocked(exitCtx, input.ChatID, false)
			out.Waiting = true
			return out, nil
		case <-deadline.C:
			if notComing := machineNotComing(ctx, router, userID, selector); notComing != nil {
				a.setBlocked(exitCtx, input.ChatID, false)
				return MachineResumeWakeOutput{Ready: true, Woke: out.Woke, NotComing: notComing.Error()}, nil
			}
			if input.Final {
				a.setBlocked(exitCtx, input.ChatID, false)
			}
			out.Waiting = true
			return out, nil
		case <-ticker.C:
		}
		if up, upErr := router.IsDaemonOnline(ctx, userID, selector); upErr == nil && up {
			a.setBlocked(exitCtx, input.ChatID, false)
			out.Ready = true
			return out, nil
		}
	}
}

// anyNeedsMachine reports whether any of the named tools runs on, or reads
// from, the user's machine.
func anyNeedsMachine(names []string) bool {
	for _, name := range names {
		if tools.NeedsMachine(name) {
			return true
		}
	}
	return false
}

// setBlocked records whether the run is waiting for its machine, which the
// chat surfaces as WAITING_FOR_DAEMON. Best-effort.
func (a *MachineResumeWakeActivity) setBlocked(ctx context.Context, chatID string, blocked bool) {
	if err := a.repo.SetChatDaemonBlocked(ctx, chatID, blocked); err != nil {
		logging.WarnContext(ctx, "[MachineResumeWake] Failed to update the daemon-blocked marker", "chatID", chatID, "blocked", blocked, "error", err)
	}
}
