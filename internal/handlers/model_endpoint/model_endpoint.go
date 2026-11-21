// Copyright (c) 2025 Reliant Labs
package model_endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/modelendpoints"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// routerDaemonControl adapts the daemon router to modelendpoints.DaemonControl,
// the same two operations DaemonRegistryService exposes to the browser.
type routerDaemonControl struct{ router Router }

// daemonErr reads a relay failure that says the machine is not connected as
// modelendpoints.ErrDaemonOffline. The router's own text (which gateway, why)
// is transport detail: it stays the cause, for the log, and never reaches the
// wire.
func daemonErr(err error) error {
	if errors.Is(err, toolexec.ErrLocalModelDaemonUnavailable) {
		return svcerr.WithCause(modelendpoints.ErrDaemonOffline, err)
	}
	return err
}

func (c routerDaemonControl) Refresh(ctx context.Context, userID, daemonID string) (*reliantv1.LocalModelInventory, error) {
	inv, err := c.router.RefreshLocalModels(ctx, userID, daemonID)
	return inv, daemonErr(err)
}

func (c routerDaemonControl) SetConfiguredEndpoints(ctx context.Context, userID, daemonID string, baseURLs []string) (*reliantv1.LocalModelInventory, error) {
	if baseURLs == nil {
		baseURLs = []string{}
	}
	payload, err := json.Marshal(map[string][]string{"base_urls": baseURLs})
	if err != nil {
		return nil, err
	}
	if _, err := c.router.SendDaemonCommandToDaemon(ctx, userID, daemonID, toolexec.CommandLocalModelsSetEndpoints, payload, 15000); err != nil {
		return nil, daemonErr(fmt.Errorf("writing endpoints on machine %s: %w", daemonID, err))
	}
	inv, err := c.router.RefreshLocalModels(ctx, userID, daemonID)
	return inv, daemonErr(err)
}

func endpointUser(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", svcerr.ErrUnauthenticated
	}
	return userID, nil
}
