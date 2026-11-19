// Copyright (c) 2025 Reliant Labs
package machinepool

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/temporal"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// ChatReader reads what placing a run needs about its chat. *db.Repo
// implements it.
type ChatReader interface {
	GetRunPlacementChat(ctx context.Context, chatID string) (*core.RunPlacementChat, error)
}

// invalidPlaceRunErrorType is the non-retryable ApplicationError type of a
// request that cannot be placed however often it is asked: it names no run,
// or not exactly one of a pool and labels, or its chat is gone.
const invalidPlaceRunErrorType = "InvalidRunPlacement"

// PlaceRunActivity is the PlaceRunActivityName activity: it reads the run's
// chat (owner, project, trigger placement) and places the run with Placer.
type PlaceRunActivity struct {
	Chats  ChatReader
	Placer *RunPlacer
}

// Execute places in. An unknown pool fails it with a non-retryable
// PoolNotFoundErrorType error whose message says what to do; any other
// failure is retried.
func (a PlaceRunActivity) Execute(ctx context.Context, in PlaceRunInput) (PlaceRunOutput, error) {
	chat, err := a.Chats.GetRunPlacementChat(ctx, in.ChatID)
	if errors.Is(err, core.ErrChatNotFound) {
		return PlaceRunOutput{}, temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("the run's chat %s no longer exists", in.ChatID), invalidPlaceRunErrorType, err)
	}
	if err != nil {
		return PlaceRunOutput{}, err
	}
	out, err := a.Placer.PlaceRun(ctx, RunRequest{
		PlaceRunInput: in,
		UserID:        chat.UserID,
		ProjectID:     chat.ProjectID,
		ChatPoolID:    chat.MachinePoolID,
		ChatDaemonID:  chat.ActiveDaemonID,
	})
	switch {
	case errors.Is(err, core.ErrMachinePoolNotFound):
		return PlaceRunOutput{}, temporal.NewNonRetryableApplicationError(PoolNotFoundMessage(in.Pool), PoolNotFoundErrorType, err)
	case errors.Is(err, ErrInvalidRunRequest):
		return PlaceRunOutput{}, temporal.NewNonRetryableApplicationError(err.Error(), invalidPlaceRunErrorType, err)
	}
	return out, err
}

// PoolNotFoundMessage is what a run, or a workflow's validation, says about
// a `daemon: {pool: name}` the owner has no pool for.
func PoolNotFoundMessage(name string) string {
	return fmt.Sprintf("no machine pool named %q — create it in Settings → Machines or fix the workflow's daemon: {pool: %s}", name, name)
}
