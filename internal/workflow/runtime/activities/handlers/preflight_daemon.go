// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/automationcred"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// PreflightDaemonCheckInput is the input for the preflight daemon check activity.
type PreflightDaemonCheckInput struct {
	ChatID         string                   `json:"chat_id" reliant:"-"`
	DaemonSelector *toolexec.DaemonSelector `json:"daemon_selector,omitempty"`
	// WaitSeconds is how long this execution may wait for an offline daemon to
	// attach after waking it. 0 is a single check.
	WaitSeconds int `json:"wait_seconds,omitempty"`
	// Final marks the last slice of the workflow's wait budget: if the daemon is
	// still down when it ends, the check fails instead of reporting Waiting.
	Final bool `json:"final,omitempty"`
	// WaitBudgetSeconds is the workflow's whole wait budget, passed so the
	// terminal error can name it. The workflow owns the number; this is only
	// how the message learns it.
	WaitBudgetSeconds int `json:"wait_budget_seconds,omitempty"`
}

// PreflightDaemonCheckOutput is the output of the preflight daemon check.
type PreflightDaemonCheckOutput struct {
	DaemonAvailable bool   `json:"daemon_available"`
	DaemonID        string `json:"daemon_id,omitempty"`
	// Waiting is true when the daemon exists but has not attached yet; the
	// workflow schedules another slice.
	Waiting bool `json:"waiting,omitempty"`
}

// PreflightDaemonCheckActivity checks if a daemon is available before workflow execution.
// This fails fast with a clear error message if a daemon is required but not available.
type PreflightDaemonCheckActivity struct {
	repo         db.Repository
	toolExecutor toolexec.ToolExecutor
	// pollInterval is how often a waiting slice re-checks the daemon; zero
	// means preflightPollInterval. Tests shrink it.
	pollInterval time.Duration
}

const preflightPollInterval = 2 * time.Second

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
	attended := true
	if ev, evErr := a.repo.GetTriggerEventByChatID(ctx, input.ChatID); evErr == nil && ev != nil &&
		ev.Kind.Unattended() {
		ctx = automationcred.Allow(ctx)
		attended = false
	}

	online, err := router.IsDaemonOnline(ctx, project.UserID, input.DaemonSelector)
	if err != nil {
		// Infrastructure error — don't fail, let it proceed and fail at tool execution.
		return PreflightDaemonCheckOutput{DaemonAvailable: true}, nil
	}
	// Marker writes on exit paths must survive a cancelled ctx.
	exitCtx := context.WithoutCancel(ctx)

	if online {
		a.machineReady(exitCtx, input.ChatID)
		return PreflightDaemonCheckOutput{DaemonAvailable: true}, nil
	}

	// Offline. Preflight is the run's wake point: the only place, besides an
	// attended send, that may resume a suspended daemon. It wakes at most once
	// per execution, then waits for the daemon to attach.
	waker, ok := router.(toolexec.DaemonWaker)
	if !ok {
		return a.terminal(exitCtx, input.ChatID, attended, errPreflightNoDaemon(""))
	}
	daemonID, wakeErr := waker.EnsureAwake(ctx, project.UserID, input.DaemonSelector)
	if wakeErr != nil && !toolexec.IsDaemonPending(wakeErr) {
		return a.terminal(exitCtx, input.ChatID, attended, errPreflightNoDaemon(wakeErr.Error()))
	}
	// A machine that failed to start is not on its way up, however long the
	// run waits: only its owner (Try again) or the control plane can rebuild
	// it.
	if notComing := machineNotComing(ctx, router, project.UserID, input.DaemonSelector); notComing != nil {
		return a.terminal(exitCtx, input.ChatID, attended, notComing)
	}

	// The daemon exists and is on its way up: this run waits rather than fails.
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
		if input.WaitSeconds > 0 {
			select {
			case <-ctx.Done():
				// Cancelled or worker stopping: a continuing run re-sets the
				// marker in its next slice, a cancelled one must not keep it.
				a.setBlocked(exitCtx, input.ChatID, false)
				return PreflightDaemonCheckOutput{Waiting: true}, nil
			case <-deadline.C:
				return a.sliceEnd(ctx, exitCtx, router, project.UserID, attended, input)
			case <-ticker.C:
			}
			if up, upErr := router.IsDaemonOnline(ctx, project.UserID, input.DaemonSelector); upErr == nil && up {
				a.setBlocked(exitCtx, input.ChatID, false)
				a.machineReady(exitCtx, input.ChatID)
				return PreflightDaemonCheckOutput{DaemonAvailable: true, DaemonID: daemonID}, nil
			}
			continue
		}
		return a.sliceEnd(ctx, exitCtx, router, project.UserID, attended, input)
	}
}

// sliceEnd closes a wait slice that ended with the daemon still down. A
// machine that failed (or was removed) during the slice ends the wait; so does
// the final slice of the workflow's budget. Otherwise it reports Waiting and
// the workflow decides when to look again.
func (a *PreflightDaemonCheckActivity) sliceEnd(ctx, exitCtx context.Context, router toolexec.DaemonRouter, userID string, attended bool, input PreflightDaemonCheckInput) (PreflightDaemonCheckOutput, error) {
	if notComing := machineNotComing(ctx, router, userID, input.DaemonSelector); notComing != nil {
		return a.terminal(exitCtx, input.ChatID, attended, notComing)
	}
	if !input.Final {
		return PreflightDaemonCheckOutput{Waiting: true}, nil
	}
	return a.terminal(exitCtx, input.ChatID, attended,
		fmt.Errorf("Your machine didn't come online %s. Check that it is running (or start it from the machines page)", waitBudgetPhrase(input.WaitBudgetSeconds)))
}

// terminal ends the run's wait for its machine with cause, the text the
// user is shown. The run that returns it fails, but an attended run's message
// is not dropped with it: the chat is marked as holding a message queued for
// its machine, and the api-server starts a run to deliver it once the machine
// connects (internal/queueddelivery). An unattended run's failure is its
// trigger's to report, as before.
func (a *PreflightDaemonCheckActivity) terminal(ctx context.Context, chatID string, attended bool, cause error) (PreflightDaemonCheckOutput, error) {
	a.setBlocked(ctx, chatID, false)
	if !attended {
		return PreflightDaemonCheckOutput{}, cause
	}
	if _, err := a.repo.SetChatQueuedForMachine(ctx, chatID, true); err != nil {
		logging.Error("[PreflightDaemonCheck] Failed to queue the message for the machine; the user will have to send it again",
			"chatID", chatID, "error", err)
		return PreflightDaemonCheckOutput{}, cause
	}
	return PreflightDaemonCheckOutput{}, fmt.Errorf("%s. %s", strings.TrimRight(cause.Error(), ". "), queuedForMachineNote)
}

// queuedForMachineNote is appended to every terminal wait failure of an
// attended run: the message stays queued (see terminal).
const queuedForMachineNote = "Your message is queued and will be sent as soon as your machine connects"

// waitBudgetPhrase renders the workflow's wait budget for the terminal
// message: "within 10 minutes", "within 6 hours".
func waitBudgetPhrase(budgetSeconds int) string {
	switch budget := time.Duration(budgetSeconds) * time.Second; {
	case budget >= 2*time.Hour:
		return fmt.Sprintf("within %d hours", int(budget/time.Hour))
	case budget >= 2*time.Minute:
		return fmt.Sprintf("within %d minutes", int(budget/time.Minute))
	case budget >= time.Minute:
		return "within a minute"
	default:
		return "in time"
	}
}

// machineStateReader is the registry read that tells a machine still coming
// up from one that is not coming: failed to start, or removed. Declared here,
// at the consumer; *toolexec.NATSDaemonRouter implements it. A router that
// does not is read as always coming up.
type machineStateReader interface {
	MachineState(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (toolexec.MachineState, error)
}

// machineNotComing returns the user-facing reason the machine this run waits
// for will not attach on its own, or nil while it may still come up. A failed
// registry read is not a reason: the wait goes on and asks again.
func machineNotComing(ctx context.Context, router toolexec.DaemonRouter, userID string, selector *toolexec.DaemonSelector) error {
	reader, ok := router.(machineStateReader)
	if !ok {
		return nil
	}
	state, err := reader.MachineState(ctx, userID, selector)
	if err != nil {
		logging.Warn("[PreflightDaemonCheck] Could not read the machine's state; still waiting for it",
			"userID", userID, "error", err)
		return nil
	}
	switch {
	case !state.Exists:
		return errors.New("This chat's machine was removed")
	case state.Failed:
		if reason := strings.TrimSpace(state.StatusMessage); reason != "" {
			return fmt.Errorf("Your machine failed to start: %s", strings.TrimRight(reason, ". "))
		}
		return errors.New("Your machine failed to start")
	default:
		return nil
	}
}

// machineReady clears a queued-for-machine marker an earlier run left: this
// run has its machine and is about to read the message, so nothing is queued
// any more. Whichever path started the run (the delivery the machine's
// connect triggered, or the user's own send) normally cleared it already; this
// is the backstop that keeps a stale marker from outliving the run that
// answered it. A cheap UPDATE that matches no row when there is none.
func (a *PreflightDaemonCheckActivity) machineReady(ctx context.Context, chatID string) {
	if _, err := a.repo.SetChatQueuedForMachine(ctx, chatID, false); err != nil {
		logging.Warn("[PreflightDaemonCheck] Failed to clear the queued-for-machine marker", "chatID", chatID, "error", err)
	}
}

// setBlocked records whether the run is parked waiting for its machine, which
// the chat list surfaces as WAITING_FOR_DAEMON. Best-effort.
func (a *PreflightDaemonCheckActivity) setBlocked(ctx context.Context, chatID string, blocked bool) {
	if err := a.repo.SetChatDaemonBlocked(ctx, chatID, blocked); err != nil {
		logging.Warn("[PreflightDaemonCheck] Failed to update daemon-blocked marker", "chatID", chatID, "blocked", blocked, "error", err)
	}
}

func errPreflightNoDaemon(cause string) error {
	msg := "this workflow requires a daemon but none is available. Start one locally with 'reliant daemon start' or deploy a cloud daemon"
	if cause != "" {
		msg += ": " + cause
	}
	return errors.New(msg)
}
