// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
)

// selections is the chat selection service over the repository. Built per use,
// not held: it is two words, and a Service built as a struct literal in a test
// needs no extra wiring.
func (s *Service) selections() chatselection.Service {
	return chatselection.New(s.deps.Repo)
}

// recordSelection stores what a send or an edit actually gave the chat's run, so
// every later run-building path and every device starts from it.
//
//   - sent is the request's params. They are stored as the user chose them
//     (normalized the way a run's inputs are), not as the run holds them: the
//     run's copy also carries preset and schema defaults, which would then be
//     frozen into the selection.
//   - moved is every model input the send-time policy (#685) moved off a
//     provider that cannot serve it, and effective the inputs after the move.
//     The moved value replaces the one the user chose, so the selection says
//     what runs and the next send neither moves it again nor announces it again.
//
// Best-effort: the run already has its inputs, and a failed write costs only the
// next send's starting point. A version conflict is the exception callers ask
// for with expectedVersion; they get the error.
func (s *Service) recordSelection(
	ctx context.Context,
	userID string,
	chat *db.Chat,
	workflowName string,
	sent map[string]*structpb.Value,
	effective map[string]interface{},
	moved []launch.ModelSubstitution,
	expectedVersion int64,
) (*chatselection.Selection, error) {
	patch := s.launcher().SelectionPatch(ctx, userID, chat.ProjectID, workflowName, sent)
	if len(moved) > 0 {
		stored, _ := s.selections().Get(ctx, chat.ID)
		applyMovesToPatch(patch, stored, effective, moved)
	}
	if len(patch) == 0 && expectedVersion == 0 {
		return nil, nil
	}
	before, _ := s.selections().Get(ctx, chat.ID)
	sel, err := s.selections().Merge(ctx, chat.ID, userID, workflowName, patch, expectedVersion)
	if err != nil {
		if errors.Is(err, chatselection.ErrVersionConflict) {
			return nil, err
		}
		logging.Warn("Failed to record the chat's selection", "error", err, "chatID", chat.ID)
		return nil, nil
	}
	if sel != nil && (before == nil || before.Version != sel.Version) {
		s.announceSelection(ctx, userID, chat, sel)
	}
	return sel, nil
}

// applyMovesToPatch writes each moved model input's value (from effective) into
// patch. A group's model ("<group>.model") replaces just that field of the
// group, over what the patch or the stored selection already holds for it.
func applyMovesToPatch(patch map[string]interface{}, stored *chatselection.Selection, effective map[string]interface{}, moved []launch.ModelSubstitution) {
	for _, sub := range moved {
		top, rest, nested := strings.Cut(sub.Input, ".")
		if !nested {
			if value, ok := effective[top]; ok {
				patch[top] = value
			}
			continue
		}
		group, ok := effective[top].(map[string]interface{})
		if !ok {
			continue
		}
		field, ok := group[rest]
		if !ok {
			continue
		}
		merged := map[string]interface{}{}
		if existing, ok := patch[top].(map[string]interface{}); ok {
			for k, v := range existing {
				merged[k] = v
			}
		} else if stored != nil {
			if existing, ok := stored.Params[top].(map[string]interface{}); ok {
				for k, v := range existing {
					merged[k] = v
				}
			}
		}
		merged[rest] = field
		patch[top] = merged
	}
}

// announceSelection tells the user's other clients that the selection changed,
// so a composer open on another device re-seeds from it. Best-effort.
func (s *Service) announceSelection(ctx context.Context, userID string, chat *db.Chat, sel *chatselection.Selection) {
	data, err := json.Marshal(map[string]interface{}{
		"chat_id":   chat.ID,
		"selection": selectionJSON(sel),
	})
	if err != nil {
		return
	}
	if err := s.deps.Repo.CreateUserUpdate(ctx, &db.UserUpdate{
		UserID:     userID,
		ProjectID:  &chat.ProjectID,
		WorktreeID: chat.WorktreeID,
		ChatID:     &chat.ID,
		UpdateType: db.UserUpdateChatConfigChanged,
		EntityType: db.EntityTypeChat,
		EntityID:   chat.ID,
		Data:       data,
	}); err != nil {
		logging.Warn("Failed to announce the chat's selection", "error", err, "chatID", chat.ID)
	}
}

// selectionJSON is the selection as a user update carries it: the same fields
// as the ChatSelection message, snake-cased.
func selectionJSON(sel *chatselection.Selection) map[string]interface{} {
	policy := map[string]interface{}{}
	if sel.Policy.OnLimit != chatselection.OnLimitInherit {
		policy["on_limit"] = string(sel.Policy.OnLimit)
	}
	if sel.Policy.FallbackModel != "" {
		policy["fallback_model"] = sel.Policy.FallbackModel
	}
	return map[string]interface{}{
		"workflow_name":   sel.WorkflowName,
		"params":          sel.Params,
		"provider_policy": policy,
		"version":         sel.Version,
		"updated_at":      sel.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// chatToProtoWithSelection is chatToProto plus the chat's selection, for the
// responses a client builds a composer from (StartChat, BranchChat, GetChat).
// A read failure leaves the selection out: the composer then shows the
// workflow's defaults, which is what the server runs when it cannot read it.
func (s *Service) chatToProtoWithSelection(ctx context.Context, chat *db.Chat) *reliantv1.Chat {
	out := chatToProto(chat)
	sel, err := s.selections().Get(ctx, chat.ID)
	if err != nil {
		logging.Warn("Failed to read the chat's selection", "error", err, "chatID", chat.ID)
		return out
	}
	out.Selection = selectionToProto(sel)
	return out
}

// selectionToProto is the wire form of a selection; nil for none.
func selectionToProto(sel *chatselection.Selection) *reliantv1.ChatSelection {
	if sel == nil {
		return nil
	}
	params, err := structpb.NewStruct(sel.Params)
	if err != nil {
		logging.Warn("A chat's stored selection params do not encode; returning it without them", "error", err, "chatID", sel.ChatID)
		params = &structpb.Struct{}
	}
	out := &reliantv1.ChatSelection{
		WorkflowName:   sel.WorkflowName,
		Params:         params,
		ProviderPolicy: policyToProto(sel.Policy),
		Version:        sel.Version,
		UpdatedAt:      sel.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	return out
}

func policyToProto(p chatselection.ProviderPolicy) *reliantv1.ProviderPolicy {
	out := &reliantv1.ProviderPolicy{}
	switch p.OnLimit {
	case chatselection.OnLimitWait:
		out.OnLimit = reliantv1.ProviderPolicy_ON_LIMIT_WAIT
	case chatselection.OnLimitFallback:
		out.OnLimit = reliantv1.ProviderPolicy_ON_LIMIT_FALLBACK
	}
	if p.FallbackModel != "" {
		model := p.FallbackModel
		out.FallbackModel = &model
	}
	return out
}

func policyFromProto(p *reliantv1.ProviderPolicy) chatselection.ProviderPolicy {
	if p == nil {
		return chatselection.ProviderPolicy{}
	}
	out := chatselection.ProviderPolicy{FallbackModel: strings.TrimSpace(p.GetFallbackModel())}
	switch p.GetOnLimit() {
	case reliantv1.ProviderPolicy_ON_LIMIT_WAIT:
		out.OnLimit = chatselection.OnLimitWait
	case reliantv1.ProviderPolicy_ON_LIMIT_FALLBACK:
		out.OnLimit = chatselection.OnLimitFallback
	}
	return out
}

// validatePolicy refuses a policy that cannot mean anything: a fallback model
// with no fallback, or a fallback with nothing to fall back to.
func validatePolicy(p chatselection.ProviderPolicy) error {
	if p.FallbackModel != "" && p.OnLimit != chatselection.OnLimitFallback {
		return fmt.Errorf("fallback_model is only meaningful with on_limit FALLBACK")
	}
	if p.OnLimit == chatselection.OnLimitFallback && !strings.Contains(p.FallbackModel, "@") {
		return fmt.Errorf("on_limit FALLBACK needs a fallback_model of the form <model>@<driver>")
	}
	return nil
}

// versionConflictError is the refusal for an edit made against a selection that
// has since changed: ABORTED, so the client refetches and decides again.
func versionConflictError() error {
	return connect.NewError(connect.CodeAborted, errors.New("the chat's selection changed since it was read; reload and try again"))
}

// UpdateChatProviderPolicy stores what the chat does when its provider is
// rate-limited, and hands it to the chat's live root run (see
// signalProviderPolicy), so the next limit the run meets follows it. The run's
// sub-agents inherit it the next time they are spawned.
func (s *Service) UpdateChatProviderPolicy(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateChatProviderPolicyRequest],
) (*connect.Response[reliantv1.UpdateChatProviderPolicyResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("chat_id is required"))
	}
	chat, err := s.getChatForUser(ctx, req.Msg.ChatId, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("chat not found"))
	}
	policy := policyFromProto(req.Msg.ProviderPolicy)
	if err := validatePolicy(policy); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	workflowName := ""
	if chat.WorkflowName != nil {
		workflowName = *chat.WorkflowName
	}
	before, _ := s.selections().Get(ctx, chat.ID)
	sel, err := s.selections().SetPolicy(ctx, chat.ID, userID, workflowName, policy, req.Msg.GetExpectedVersion())
	if err != nil {
		if errors.Is(err, chatselection.ErrVersionConflict) {
			return nil, versionConflictError()
		}
		logging.Error("Failed to store the chat's provider policy", "error", err, "chatID", chat.ID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to update provider policy"))
	}
	if sel != nil && (before == nil || before.Version != sel.Version) {
		s.announceSelection(ctx, userID, chat, sel)
		s.signalProviderPolicy(ctx, userID, chat)
	}
	return connect.NewResponse(&reliantv1.UpdateChatProviderPolicyResponse{Selection: selectionToProto(sel)}), nil
}

// underSelection lays the chat's stored selection (for workflowName) UNDER
// update: a key the update carries wins, whole. It is the base of a resume's
// input update, where the update is the send's.
func (s *Service) underSelection(ctx context.Context, chatID, workflowName string, update map[string]interface{}) map[string]interface{} {
	params := s.launcher().ChatSelectionParams(ctx, chatID, workflowName)
	if len(params) == 0 {
		return update
	}
	out := make(map[string]interface{}, len(params)+len(update))
	for key, value := range params {
		out[key] = value
	}
	for key, value := range update {
		out[key] = value
	}
	return out
}

// overSelection lays the chat's stored selection OVER a stopped run's recorded
// inputs. Those inputs say what the run last ran on, the selection what the chat
// is set to now; a pick made after the run stopped must reach the run that
// continues it.
func (s *Service) overSelection(ctx context.Context, chatID, workflowName string, recorded map[string]interface{}) map[string]interface{} {
	params := s.launcher().ChatSelectionParams(ctx, chatID, workflowName)
	if len(params) == 0 {
		return recorded
	}
	out := make(map[string]interface{}, len(params)+len(recorded))
	for key, value := range recorded {
		out[key] = value
	}
	for key, value := range params {
		out[key] = value
	}
	return out
}

// selectionStoredModelIsCurrent builds the isNews test for a fresh run's model
// moves: a move is news only when the chat's stored selection does not already
// hold the moved value. The composer re-seeds from the selection, but a stale
// client, or a send with no params, can still arrive carrying the original pin;
// the run moves it again, and announcing the same move every turn is noise.
func (s *Service) selectionStoredModelIsCurrent(ctx context.Context, chatID string, inputs map[string]interface{}) func(topLevelInput string) bool {
	stored, err := s.selections().Get(ctx, chatID)
	if err != nil || stored == nil {
		return nil
	}
	return func(top string) bool {
		return !paramsContain(stored.Params[top], inputs[top])
	}
}

// paramsContain reports whether have holds everything want does: equal values,
// and for maps every key of want present and contained.
func paramsContain(have, want interface{}) bool {
	wantMap, wantIsMap := want.(map[string]interface{})
	haveMap, haveIsMap := have.(map[string]interface{})
	if wantIsMap && haveIsMap {
		for key, value := range wantMap {
			if !paramsContain(haveMap[key], value) {
				return false
			}
		}
		return true
	}
	haveJSON, errHave := json.Marshal(have)
	wantJSON, errWant := json.Marshal(want)
	return errHave == nil && errWant == nil && string(haveJSON) == string(wantJSON)
}

// changedPresets is the part of a request's preset selection that differs from
// the chat's current one. Empty names are the absence of a selection.
func changedPresets(current, requested map[string]string) map[string]string {
	var changed map[string]string
	for key, name := range requested {
		if name != "" && current[key] != name {
			if changed == nil {
				changed = map[string]string{}
			}
			changed[key] = name
		}
	}
	return changed
}
