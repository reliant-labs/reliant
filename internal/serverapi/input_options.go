// Copyright (c) 2025 Reliant Labs

package serverapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/trigger"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/credwire"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions"
	"github.com/reliant-labs/reliant/internal/netguard"
)

// wireInputOptions builds the service behind CatalogService.ListInputOptions
// and enable-time reachability checks: an integration type's list action run
// as the signed-in user, through the same credential rules a run's actions
// follow (credwire), with the shipped catalog's types.
//
// The API server resolves the user's connection itself rather than asking the
// worker: a picker is a read the user is waiting on, and the resolver only
// needs the database and the vault key this process already holds.
func wireInputOptions(repo *db.Repo, tokens *connections.TokenSource, cp controlplane.Endpoint) (inputoptions.Service, error) {
	idx, err := catalogindex.Builtin()
	if err != nil {
		return nil, fmt.Errorf("input options: %w", err)
	}
	creds, err := credwire.New(connections.NewResolver(repo, repo.Connections(), tokens), cp)
	if err != nil {
		return nil, err
	}
	return inputoptions.NewInstrumented(inputoptions.Deps{
		Types:       idx,
		Runner:      httpaction.NewRunner(netguard.New()),
		Credentials: creds,
	})
}

// optionsChecker answers trigger.OptionsChecker from the option service, so
// enabling an automation asks the integration the same question its picker did.
//
// Only a definite answer refuses an enable. A value the integration cannot
// reach for this owner is Unreachable; one the acting identity cannot write to
// is a warning. Anything that stops the question being answered (the provider
// down or rate limited, no usable connection) is an error, which the trigger
// service turns into a warning: a check that cannot be made is not evidence
// about the value.
type optionsChecker struct{ svc inputoptions.Service }

func (c optionsChecker) CheckValues(ctx context.Context, check trigger.ValueCheck) (trigger.ValueCheckResult, error) {
	res, err := c.svc.Resolve(ctx, inputoptions.ResolveRequest{
		UserID: check.UserID, Type: check.Type, ConnectionID: check.ConnectionID, Scope: check.Scope,
		Values: check.Values, ActingIdentity: string(check.ActingIdentity),
	})
	if err != nil {
		var upstream *inputoptions.UpstreamError
		switch {
		case errors.As(err, &upstream):
			return trigger.ValueCheckResult{}, errors.New(upstream.Message)
		case errors.Is(err, inputoptions.ErrUnknownType), errors.Is(err, inputoptions.ErrNoOptions):
			// A type with nothing to check against has nothing to refuse.
			return trigger.ValueCheckResult{}, nil
		}
		return trigger.ValueCheckResult{}, errors.New(inputoptions.Message(err))
	}
	if res.State == inputoptions.StateNeedsConnection {
		return trigger.ValueCheckResult{}, errors.New("no usable connection to check against")
	}
	out := trigger.ValueCheckResult{Unreachable: res.Unreachable}
	for _, o := range res.Options {
		for _, w := range o.Warnings {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%s: %s", o.Label, strings.TrimSpace(w)))
		}
	}
	return out, nil
}
