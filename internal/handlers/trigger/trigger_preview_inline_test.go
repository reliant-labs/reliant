// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestInlineEventPreviewIsOwnedReadOnlyAndWorksWhilePaused(t *testing.T) {
	ctx := AuthedContext(t)
	mine := auth.MustGetUserID(ctx)
	repo := &MockRepository{GetTriggerFunc: func(_ context.Context, id string) (*core.Trigger, error) {
		owner := mine
		if id == "foreign" {
			owner = "other-user"
		}
		return &core.Trigger{ID: id, UserID: owner, ProjectID: "project", Kind: core.TriggerKindWebhook, NoMachine: true, Enabled: false, Message: "Review delivery.", Config: []byte(`{}`)}, nil
	}}
	// Unconfigured repository writes, intake or launcher would panic: only the
	// owned read is installed, proving a preview never starts or enables a run.
	svc := NewTestTrigger(t, WithTriggerDeps(Deps{Repo: repo}), WithPlaceholderDeps())
	payload, err := structpb.NewStruct(map[string]any{"body": map[string]any{"ref": "release", "user_id": "attacker"}})
	require.NoError(t, err)
	id := "mine"
	req := &reliantv1.PreviewReplayRequest{TriggerId: &id, Replay: &reliantv1.ReplayEvent{Payload: payload, OccurredAt: "2026-10-27T12:00:00Z"}}
	result, err := svc.PreviewReplay(ctx, connect.NewRequest(req))
	require.NoError(t, err)
	require.Contains(t, result.Msg.Prompt, "Review delivery.")
	require.Contains(t, result.Msg.Prompt, `"ref":"release"`)
	require.Contains(t, result.Msg.Prompt, "<trigger_event")
	req.Replay.OccurredAt = "invalid-secret-marker"
	_, err = svc.PreviewReplay(ctx, connect.NewRequest(req))
	require.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	require.NotContains(t, err.Error(), "invalid-secret-marker")
	req.Replay.OccurredAt = ""
	req.Replay.Payload, err = structpb.NewStruct(map[string]any{"body": strings.Repeat("x", 65*1024)})
	require.NoError(t, err)
	_, err = svc.PreviewReplay(ctx, connect.NewRequest(req))
	require.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	id = "foreign"
	_, err = svc.PreviewReplay(ctx, connect.NewRequest(req))
	require.Equal(t, connect.CodeNotFound, svcerr.Code(err))
}
