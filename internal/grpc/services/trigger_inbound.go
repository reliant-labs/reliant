// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/vault"
)

// The inbound-trigger half of TriggerService: webhook, integration and
// workflow-event triggers. They share the row and the CRUD with schedules;
// what differs is that they have no Temporal schedule to converge, and that
// they carry credentials (a webhook token, an HMAC secret) or a connection.

// webhookSealer seals a webhook trigger's HMAC secret under its owner's
// tenant. Satisfied by *vault.Vault.
type webhookSealer interface {
	Seal(ctx context.Context, tenant vault.Tenant, plaintext, aad []byte) ([]byte, error)
}

// integrationCatalog says which integrations can deliver events. The provider
// registry (internal/integrations/webhook) satisfies it.
type integrationCatalog interface {
	HasInboundSource(integration string) bool
}

// eventIntake records a manual fire of an inbound trigger. Satisfied by
// *triggers.Intake.
type eventIntake interface {
	Accept(ctx context.Context, trigger *core.Trigger, ev triggers.InboundEvent, opts triggers.AcceptOptions) (*triggers.AcceptResult, error)
}

// InboundOptions wires the inbound kinds into TriggerService. Each field is
// optional; a kind whose dependency is missing is rejected with Unavailable
// at write time rather than stored unable to fire.
type InboundOptions struct {
	// PublicURL is this server's externally reachable base, used to render
	// webhook URLs.
	PublicURL string
	Sealer    webhookSealer
	Catalog   integrationCatalog
	Intake    eventIntake
}

// WithInbound enables webhook, integration and workflow-event triggers.
func (s *TriggerService) WithInbound(opts InboundOptions) *TriggerService {
	s.inbound = opts
	return s
}

// inboundUnavailable reports a kind this deployment cannot serve.
func inboundUnavailable(what string) error {
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s triggers are not available on this server", what))
}

// applyInboundDefinition fills the kind-specific fields of an inbound
// trigger from def, validating what the source needs. existing is the stored
// row on update.
func (s *TriggerService) applyInboundDefinition(ctx context.Context, userID string, def *reliantv1.TriggerDefinition, src *triggers.Source, trigger *core.Trigger, existing *core.Trigger) error {
	if strings.TrimSpace(def.Filter) != "" {
		if _, err := triggers.CompileFilter(def.Filter); err != nil {
			var fe *triggers.FilterError
			if errors.As(err, &fe) {
				return connect.NewError(connect.CodeInvalidArgument, fe)
			}
			return connect.NewError(connect.CodeInternal, err)
		}
		trigger.Filter = strings.TrimSpace(def.Filter)
	}

	switch src.Kind {
	case core.TriggerKindWebhook:
		if s.inbound.Intake == nil {
			return inboundUnavailable("webhook")
		}
		if src.HMAC && s.inbound.Sealer == nil {
			return inboundUnavailable("signed webhook")
		}
		hasSecret := existing != nil && existing.Kind == core.TriggerKindWebhook && s.storedSecret(ctx, existing.ID)
		if src.HMAC && def.WebhookHmacSecret == nil && !hasSecret {
			return connect.NewError(connect.CodeInvalidArgument,
				errors.New("webhook_hmac_secret is required to verify signatures"))
		}
	case core.TriggerKindIntegration:
		if s.inbound.Intake == nil || s.inbound.Catalog == nil {
			return inboundUnavailable("integration")
		}
		if !s.inbound.Catalog.HasInboundSource(src.Integration) {
			return connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("integration %q does not deliver events on this server", src.Integration))
		}
		connID, err := s.resolveTriggerConnection(ctx, userID, src.Integration, def.ConnectionId)
		if err != nil {
			return err
		}
		trigger.ConnectionID = &connID
	case core.TriggerKindWorkflowEvent:
		// Stream C delivers these; nothing here is credentialed.
	}
	return nil
}

// resolveTriggerConnection picks the connection an integration trigger listens
// through: the named one, which must be the caller's and for this
// integration, or the caller's default for it. A foreign id is NotFound,
// exactly like a missing one.
func (s *TriggerService) resolveTriggerConnection(ctx context.Context, userID, integration string, requested *string) (string, error) {
	store := s.connections()
	if store == nil {
		return "", inboundUnavailable("integration")
	}
	var (
		conn *core.Connection
		err  error
	)
	if requested != nil && *requested != "" {
		conn, err = store.GetConnection(ctx, userID, *requested)
	} else {
		conn, err = store.DefaultConnection(ctx, userID, integration)
	}
	if err != nil || conn == nil || conn.UserID != userID {
		if requested == nil || *requested == "" {
			return "", connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("connect %s first: an integration trigger listens through one of your connections", integration))
		}
		return "", connect.NewError(connect.CodeNotFound, errors.New("connection not found"))
	}
	if conn.IntegrationID != integration {
		return "", connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("connection %q is for %s, not %s", conn.Name, conn.IntegrationID, integration))
	}
	if conn.Status == core.ConnectionStatusRevoked {
		return "", connect.NewError(connect.CodeNotFound, errors.New("connection not found"))
	}
	return conn.ID, nil
}

func (s *TriggerService) storedSecret(ctx context.Context, triggerID string) bool {
	creds, err := s.database.GetTriggerWebhookCredentials(ctx, triggerID)
	return err == nil && len(creds.SecretSealed) > 0
}

// writeWebhookCredentials issues a token for a new webhook trigger and
// stores its HMAC secret. It runs after the row exists, because the secret's
// sealing is bound to the trigger id.
func (s *TriggerService) writeWebhookCredentials(ctx context.Context, trigger *core.Trigger, def *reliantv1.TriggerDefinition, issueToken bool) (*reliantv1.WebhookCredential, error) {
	var cred *reliantv1.WebhookCredential
	if issueToken {
		var err error
		cred, err = s.issueWebhookToken(ctx, trigger.ID)
		if err != nil {
			return nil, err
		}
	}
	cfg, err := triggers.WebhookConfigFor(trigger)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	switch {
	case cfg.HMAC == nil:
		// Signatures off: drop any secret left from an earlier definition.
		if err := s.database.SetTriggerWebhookSecret(ctx, trigger.ID, nil); err != nil {
			return nil, triggerDBError("clear webhook secret", err)
		}
	case def.WebhookHmacSecret != nil:
		if *def.WebhookHmacSecret == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("webhook_hmac_secret must not be empty"))
		}
		sealed, err := s.inbound.Sealer.Seal(ctx, vault.UserTenant(trigger.UserID),
			[]byte(*def.WebhookHmacSecret), triggers.WebhookSecretAAD(trigger.ID))
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("seal webhook secret: %w", err))
		}
		if err := s.database.SetTriggerWebhookSecret(ctx, trigger.ID, sealed); err != nil {
			return nil, triggerDBError("set webhook secret", err)
		}
	}
	return cred, nil
}

func (s *TriggerService) issueWebhookToken(ctx context.Context, triggerID string) (*reliantv1.WebhookCredential, error) {
	token, hash, err := triggers.NewWebhookToken()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := s.database.SetTriggerWebhookTokenHash(ctx, triggerID, hash); err != nil {
		return nil, triggerDBError("set webhook token", err)
	}
	return &reliantv1.WebhookCredential{
		Token: token,
		Url:   triggers.WebhookURL(s.inbound.PublicURL, triggerID) + "/" + token,
	}, nil
}

// RotateWebhookToken replaces a webhook trigger's token.
func (s *TriggerService) RotateWebhookToken(
	ctx context.Context,
	req *connect.Request[reliantv1.RotateWebhookTokenRequest],
) (*connect.Response[reliantv1.RotateWebhookTokenResponse], error) {
	trigger, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if trigger.Kind != core.TriggerKindWebhook {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("only a webhook trigger has a token"))
	}
	cred, err := s.issueWebhookToken(ctx, trigger.ID)
	if err != nil {
		return nil, err
	}
	logging.Info("webhook trigger token rotated", "trigger_id", trigger.ID, "user_id", trigger.UserID)
	return connect.NewResponse(&reliantv1.RotateWebhookTokenResponse{
		Trigger: s.render(ctx, trigger),
		Webhook: cred,
	}), nil
}

// fireInbound is "run now" for an inbound trigger: a manual event with an
// empty payload except the marker, recorded and launched like any delivery.
func (s *TriggerService) fireInbound(ctx context.Context, trigger *core.Trigger) (string, error) {
	if s.inbound.Intake == nil {
		return "", inboundUnavailable(string(trigger.Kind))
	}
	res, err := s.inbound.Intake.Accept(ctx, trigger, triggers.InboundEvent{
		Kind:      trigger.Kind.EventKind(),
		DedupeKey: trigger.ID + ":manual-" + uuid.NewString(),
		Payload:   map[string]any{"manual": true, "requested_by": auth.MustGetUserID(ctx)},
	}, triggers.AcceptOptions{Manual: true})
	if err != nil {
		return "", connect.NewError(connect.CodeUnavailable, fmt.Errorf("record fire: %w", err))
	}
	return triggers.EventFireWorkflowID(res.EventID), nil
}
