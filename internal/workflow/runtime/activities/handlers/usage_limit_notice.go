// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/accountrouter"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// The transcript notices that make account and provider moves visible. Each is
// a warning-styled system message on the thread that moved, and each is
// best-effort: the move already happened, so a notice that cannot be saved is
// logged and the conversation goes on.

// noticeProviderName is the short provider name notices use ("Claude", not the
// settings label "Claude subscription").
func noticeProviderName(driver string) string {
	switch driver {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "anthropic":
		return "Anthropic"
	}
	return models.ProviderDisplayName(driver)
}

// noticeModelName is "<model name> · <provider>" for a "<model>@<driver>" id.
func noticeModelName(modelAtDriver string) string {
	id, driver := modelAtDriver, ""
	for i := len(modelAtDriver) - 1; i >= 0; i-- {
		if modelAtDriver[i] == '@' {
			id, driver = modelAtDriver[:i], modelAtDriver[i+1:]
			break
		}
	}
	name := id
	if def, ok := models.MustGetRegistry().GetDefinition(id); ok && def.Name != "" {
		name = def.Name
	}
	if driver == "" {
		return name
	}
	return name + " · " + noticeProviderName(driver)
}

func noticeWindowName(window string) string {
	switch window {
	case "five_hour", "5h":
		return "5-hour"
	case "seven_day", "7d":
		return "7-day"
	case "":
		return ""
	}
	return window
}

// noticeClock is a time inside a notice's text: an RFC 3339 UTC instant in a
// <time:...> token. The worker does not know the reader's time zone (it runs in
// UTC), so it stores the instant and the web renders it in the reader's zone
// (web/src/lib/providerNotice.ts). Read as plain text, as the model reads it,
// the token is still an unambiguous time.
func noticeClock(t time.Time) string {
	return "<time:" + t.UTC().Format(time.RFC3339) + ">"
}

// accountMoveNotice is "Claude account alice@acme.com reached its 5-hour limit
// (resets 3:40 PM). Continuing on bob@acme.com." for a thread that moved to
// another account of the same provider.
func accountMoveNotice(res accountrouter.Result, window string, resetAt time.Time) string {
	driver := string(res.Account.DriverID)
	from := "an account"
	if res.Previous != nil {
		if l := accountName(*res.Previous); l != "" {
			from = l
		}
	}
	to := accountName(res.Account)
	provider := noticeProviderName(driver)

	var why string
	switch res.Reason {
	case accountrouter.ReasonLimited:
		why = "reached its limit"
		if w := noticeWindowName(window); w != "" {
			why = "reached its " + w + " limit"
		}
		if !resetAt.IsZero() {
			why += " (resets " + noticeClock(resetAt) + ")"
		}
	case accountrouter.ReasonUnserved:
		why = "cannot serve this model"
	default:
		why = "is no longer available"
	}
	msg := fmt.Sprintf("%s account **%s** %s.", provider, from, why)
	if to != "" {
		msg += fmt.Sprintf(" Continuing on **%s**.", to)
	}
	return msg
}

func accountName(c models.AccountConfig) string {
	switch {
	case c.AccountLabel != "":
		return c.AccountLabel
	case c.AccountEmail != "":
		return c.AccountEmail
	}
	return ""
}

// providerSwitchNotice is the cross-provider fallback: every account of the
// provider is limited and the chat allows (or the user asked for) another.
// probing says until is only when the stay next checks the provider, because no
// reset time is known.
func providerSwitchNotice(provider, model string, until time.Time, probing bool) string {
	msg := fmt.Sprintf("%s is rate-limited on all your accounts", noticeProviderName(provider))
	switch {
	case until.IsZero():
	case probing:
		msg += "; no reset time was given, checking again at " + noticeClock(until)
	default:
		msg += " until " + noticeClock(until)
	}
	msg += fmt.Sprintf(". Continuing on **%s** as this chat allows", noticeModelName(model))
	if provider != "" {
		if probing {
			msg += fmt.Sprintf("; returning to %s once its limit lifts", noticeProviderName(provider))
		} else {
			msg += fmt.Sprintf("; returning to %s after the reset", noticeProviderName(provider))
		}
	}
	return msg + "."
}

// providerReturnNotice is the chat coming back to its own provider.
func providerReturnNotice(provider, model string) string {
	return fmt.Sprintf("%s's limit has reset. Back on **%s**.", noticeProviderName(provider), noticeModelName(model))
}

// postNotice saves text as a warning notice on thread. Best-effort, and
// idempotent on key when one is given: a retried activity writes the notice
// once.
func postNotice(ctx context.Context, repo threads.Repository, chatID, thread, workflowID, key, text string) {
	if repo == nil || chatID == "" || thread == "" || text == "" {
		return
	}
	var wf *string
	if workflowID != "" {
		wf = &workflowID
	}
	opts := threads.SaveMessageOpts{
		ChatID:       chatID,
		Thread:       thread,
		Role:         int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM),
		Content:      text,
		DisplayStyle: int32(reliantv1.DisplayStyle_DISPLAY_STYLE_WARNING),
		WorkflowID:   wf,
	}
	if key != "" {
		opts.ActivityID = &key
		opts.AttemptNumber = 1
	}
	if _, err := threads.NewService(repo).SaveMessage(ctx, opts); err != nil {
		logging.WarnContext(ctx, "[Failover] Could not save the provider notice; continuing",
			"chatID", chatID, "thread", thread, "error", err)
	}
}

// noticeStepKey keys a step's notices so a retried activity writes each once.
func noticeStepKey(rtx types.RuntimeContext) string {
	if rtx.MessageIdempotencyKey != "" {
		return rtx.MessageIdempotencyKey
	}
	return rtx.WorkflowID + "-" + rtx.StepID
}

// announceRouting saves the notice a resolved call owes the transcript before
// its stream starts: the thread moved to another account of its provider.
func (a *CallLLMActivity) announceRouting(ctx context.Context, chat *db.Chat, thread string, rtx types.RuntimeContext, resolved *resolvedLLMCall) {
	if resolved.Account != nil {
		a.routing.announceAccountMove(ctx, func(text string) {
			postNotice(ctx, a.repo, chat.ID, thread, rtx.WorkflowID,
				noticeStepKey(rtx)+"-account-"+resolved.Account.Config.AccountID, text)
		}, chat.UserID, resolved.Account)
	}
}

// announceReturn says the chat is back on its own provider after a stay on a
// fallback. It is posted only once the turn has been served by it: the stay's
// end is a time to look again, not proof the limit lifted, and a turn that the
// provider refuses is the stay going on, not a return.
func (a *CallLLMActivity) announceReturn(ctx context.Context, chat *db.Chat, thread string, rtx types.RuntimeContext, resolved *resolvedLLMCall) {
	ret := rtx.FallbackReturn
	if ret == nil || rtx.FallbackModel != "" {
		return
	}
	own := resolved.ProviderDriver
	if resolved.Definition != nil {
		own = resolved.Definition.ID + "@" + resolved.ProviderDriver
	}
	postNotice(ctx, a.repo, chat.ID, thread, rtx.WorkflowID, noticeStepKey(rtx)+"-return",
		providerReturnNotice(ret.Provider, own))
}
