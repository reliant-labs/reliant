package chat

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// UpdateWorkflowParams updates workflow parameters for a running chat
// This signals the running workflow to update its inputs (e.g., mode, temperature)
func (s *Service) UpdateWorkflowParams(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateWorkflowParamsRequest],
) (*connect.Response[reliantv1.UpdateWorkflowParamsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat to verify ownership
	chat, err := s.chatViews().GetChat(ctx, req.Msg.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	if !authz.CanOwnChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("chat")
	}

	// Always signal the root workflow. Child workflows are inline goroutines
	// with synthetic DB IDs that Temporal doesn't know about.
	// Thread-scoped updates use the __thread key in the signal payload.
	workflowID := chat.MainThreadID()
	if workflowID == "" {
		return nil, svcerr.FailedPrecondition("no active workflow for chat")
	}

	if err := launch.ValidateWorkflowParamStructure(req.Msg.Params); err != nil {
		return nil, err
	}

	// If targeting a specific thread, verify there is a run of it for the
	// params to apply to.
	if req.Msg.ThreadId != nil && *req.Msg.ThreadId != "" {
		if err := s.threadAcceptsParamUpdate(ctx, chat, *req.Msg.ThreadId); err != nil {
			return nil, err
		}
	}

	workflowName := ""
	if chat.WorkflowName != nil {
		workflowName = *chat.WorkflowName
	}
	threadScoped := req.Msg.ThreadId != nil && *req.Msg.ThreadId != ""

	// The update is exactly the keys the client sent. A key it did not send is
	// unchanged to the run: the old expansion to the full preset+default set
	// reset a live run's model to the preset's tier on a Mode toggle, and
	// overwrote a sub-agent's model, tools and skills with the root's preset.
	params := req.Msg.Params
	validationChat := chat
	if threadScoped {
		wf, err := s.deps.Repo.GetWorkflowByThread(ctx, chat.ID, *req.Msg.ThreadId)
		if err != nil || wf == nil {
			return nil, svcerr.FailedPrecondition("The thread's workflow is unavailable. Try again when it resumes.")
		}
		workflowName = wf.WorkflowName
		// Never apply the root's preset or selection to a thread's reset or
		// validation. Its own baseline is maintained by the runtime.
		threadChat := *chat
		threadChat.ID = ""
		threadChat.SelectedPresets = nil
		validationChat = &threadChat
		params, err = s.resolveThreadResetParams(ctx, userID, chat, workflowName, workflowID, *req.Msg.ThreadId, params)
		if err != nil {
			return nil, err
		}
	}
	stateUpdate, err := s.launcher().BuildStateUpdateForActiveWorkflow(ctx, userID, validationChat, workflowName, nil, params)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	// Validate the run as it would be after the update (model selectors,
	// required inputs) before anything is stored or signalled.
	var validationErrors []error
	if threadScoped {
		// Required thread inputs may come from node args, not schema defaults.
		// Validate the actual thread plus the delta whenever it can answer.
		var current map[string]interface{}
		response, queryErr := rtemporal.QueryOpenRun(ctx, s.deps.Temporal, workflowID, "get_thread_inputs", *req.Msg.ThreadId)
		if queryErr == nil && response.Get(&current) == nil && current != nil {
			for key, value := range stateUpdate {
				current[key] = value
			}
			validationErrors = s.launcher().ValidateChatWorkflowInputs(ctx, userID, validationChat, workflowName, current)
		} else {
			validationErrors = s.launcher().ValidateStateUpdate(ctx, userID, validationChat, workflowName, nil, stateUpdate)
		}
	} else {
		validationErrors = s.launcher().ValidateStateUpdate(ctx, userID, validationChat, workflowName, validationChat.SelectedPresets, stateUpdate)
	}
	if len(validationErrors) > 0 {
		errMsgs := make([]string, len(validationErrors))
		for i, e := range validationErrors {
			errMsgs[i] = e.Error()
		}
		return nil, svcerr.InvalidArgument(fmt.Sprintf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")))
	}

	if threadScoped {
		// While the chat is bound to a provider every thread runs on it, so
		// a sub-agent's pick on another provider (or a local one) is refused
		// here rather than accepted and then held at its next model call.
		if value, picked := stateUpdate["model"]; picked {
			if bound := s.chatProvider(ctx, chat.ID); bound != "" {
				pick, err := s.launcher().ValidateSelectedModel(ctx, userID, value, s.selectionCheck(chat, bound))
				if err != nil {
					return nil, svcerr.FailedPrecondition("The selected model cannot be served. The agent's model has not changed: " + err.Error())
				}
				if pick.Local || pick.Driver != bound {
					return nil, svcerr.FailedPrecondition(fmt.Sprintf(
						"This chat runs on %s, so its agents do too. Pick a model %s serves for this agent, or switch the whole chat's model to change provider.", bound, bound))
				}
			}
		}
		// A thread's params are that run's state, not the chat's choice: they
		// are signalled and remembered by the run, never stored as the
		// selection (which would hand the sub-agent's model to the next root run).
		stateUpdate["__thread"] = *req.Msg.ThreadId
		var resetKeys []string
		for key, value := range req.Msg.Params {
			if value.AsInterface() == nil {
				resetKeys = append(resetKeys, key)
			}
		}
		if len(resetKeys) > 0 {
			sort.Strings(resetKeys)
			stateUpdate["__reset_keys"] = resetKeys
		}
		if err := s.signalParamUpdate(ctx, workflowID, stateUpdate); err != nil {
			return nil, err
		}
		return connect.NewResponse(&reliantv1.UpdateWorkflowParamsResponse{
			Success: true,
			Message: "Workflow parameters updated",
		}), nil
	}

	// An edit of other params still holds an auto run to its org's mode
	// floor (withLiveModeFloor).
	if stateUpdate, err = s.withLiveModeFloor(ctx, userID, chat, workflowName, stateUpdate); err != nil {
		return nil, err
	}

	// The chat's own edit. Whether or not a run is live to receive it, it is
	// the chat's choice from now on, so it is stored: the next send from any
	// device, and the next run, start from it. A live run is moved off a
	// provider that cannot serve it first, exactly as a send moves it, so what
	// is stored is what runs.
	live := s.rootRunIsLive(ctx, workflowID)
	_, explicitModelPick := req.Msg.Params["model"]
	// The provider binding is decided here and nowhere else (see
	// chat_provider_binding.go): a pick that names another provider than the
	// root runs on switches the chat to it, a local pick unbinds it, and any
	// other pick leaves it as it was.
	bound := s.chatProvider(ctx, chat.ID)
	binding := bound
	if explicitModelPick {
		pick, err := s.launcher().ValidateSelectedModel(ctx, userID, stateUpdate["model"], s.selectionCheck(chat, bound))
		if err != nil {
			return nil, svcerr.FailedPrecondition("The selected model/provider cannot be served. Your previous selection has not changed: " + err.Error())
		}
		switch {
		case pick.Local:
			binding = ""
		case pick.NamesProvider && pick.Driver != s.launcher().RootProvider(ctx, userID, chat, workflowName):
			binding = pick.Driver
		}
	}
	var moves []launch.ModelSubstitution
	if live {
		// Preserve the root picker choice, but retain send-time recovery for
		// unrelated grouped model inputs changed in the same request.
		recoverable := make(map[string]interface{}, len(stateUpdate))
		for key, value := range stateUpdate {
			if key != "model" || !explicitModelPick {
				recoverable[key] = value
			}
		}
		moves = s.moveUnservableModels(ctx, userID, chat, workflowName, workflowID, recoverable)
		for key, value := range recoverable {
			stateUpdate[key] = value
		}
	}
	var bindingPatch *string
	if explicitModelPick {
		bindingPatch = &binding
	}
	sel, err := s.recordSelection(ctx, userID, chat, workflowName, req.Msg.Params, stateUpdate, moves, req.Msg.GetExpectedVersion(), bindingPatch)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to record the chat's selection"))
	}
	if binding != bound {
		// The run learns of the switch with the model it switched to
		// (runtime applyProviderSwitch); "" unbinds it.
		stateUpdate[v2.InputKeyProviderSwitch] = binding
	}
	if sel == nil {
		sel, _ = s.selections().Get(ctx, chat.ID)
	}

	if live && len(stateUpdate) > 0 {
		// A move is news only when it changes what the run already uses.
		news := make([]launch.ModelSubstitution, 0, len(moves))
		for _, sub := range moves {
			if s.checkParamsActuallyChanged(ctx, workflowID, map[string]interface{}{topLevelInput(sub.Input): stateUpdate[topLevelInput(sub.Input)]}) {
				news = append(news, sub)
			}
		}
		s.postModelFallbackNotices(ctx, req.Msg.ChatId, workflowID, workflowID, news)
		modelChanged := false
		if newModel, ok := stateUpdate["model"]; ok {
			modelChanged = s.checkParamsActuallyChanged(ctx, workflowID, map[string]interface{}{"model": newModel})
		}
		if err := s.signalParamUpdate(ctx, workflowID, stateUpdate); err != nil {
			return nil, err
		}
		if modelChanged {
			// Switching the chat's model is a way out of a provider's limit,
			// for the whole chat: every thread blocked on a provider retries
			// now, on the new model where it follows the chat's. A thread
			// pinned to its own model on the provider it is waiting on may
			// continue on the switched-to one until that provider resets —
			// switching providers is the user opting the chat in
			// (CapacityWakeSignal.UseModel).
			s.wakeChatBestEffort(ctx, chat, s.launcher().ResolvedModelAtDriver(ctx, userID, chat, stateUpdate["model"]), "the chat's model changed")
		}
	}

	logging.DebugContext(ctx, "Updated workflow params",
		"chatID", req.Msg.ChatId,
		"workflowID", workflowID,
		"live", live,
		"paramCount", len(stateUpdate))

	return connect.NewResponse(&reliantv1.UpdateWorkflowParamsResponse{
		Success:   true,
		Message:   "Workflow parameters updated",
		Selection: selectionToProto(sel),
	}), nil
}
