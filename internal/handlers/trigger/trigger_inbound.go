// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/vault"
)

// The inbound-trigger half of TriggerService: webhook, integration and
// workflow-event triggers. They share the row and the CRUD with schedules;
// what differs is that they have no Temporal schedule to converge, and that
// they carry credentials (a webhook token, an HMAC secret) or a connection.

// WebhookSealer seals a webhook trigger's HMAC secret under its owner's
// tenant. Satisfied by vault.Service.
type WebhookSealer interface {
	Seal(ctx context.Context, tenant vault.Tenant, plaintext, aad []byte) ([]byte, error)
}

// IntegrationCatalog says which integrations can deliver events. The provider
// registry (internal/integrations/webhook) satisfies it.
type IntegrationCatalog interface {
	HasInboundSource(integration string) bool
}

// userConfiguredCatalog is the optional half of a catalog that knows which
// integrations' webhooks the user configures themselves (Twilio's per-number
// URL), so a trigger can show its owner the URL. The provider registry
// satisfies it.
type userConfiguredCatalog interface {
	UserConfiguredURL(integration string) bool
}

// EventIntake records a manual fire of an inbound trigger. Satisfied by
// *triggers.Intake.
type EventIntake interface {
	Accept(ctx context.Context, trigger *core.Trigger, ev triggers.InboundEvent, opts triggers.AcceptOptions) (*triggers.AcceptResult, error)
}

// IntegrationAccess keeps one user's access snapshot for an access-gated
// integration fresh (GitHub: which repositories, in which App installations,
// the user's own token can see). Satisfied by *ghaccess.Refresher via an
// adapter in internal/app (providers_api.go).
type IntegrationAccess interface {
	// Refresh re-reads userID's access now.
	Refresh(ctx context.Context, userID string) error
	// IsPermanent reports whether a Refresh error needs the user to act
	// (connect, reconnect) rather than a retry.
	IsPermanent(err error) bool
}

// inboundUnavailable reports a kind this deployment cannot serve.
func inboundUnavailable(what string) error {
	msg := fmt.Sprintf("%s triggers are not available on this server", what)
	return tools.RefuseActivation(msg, svcerr.Unavailable(msg))
}

// applyInboundDefinition fills the kind-specific fields of an inbound
// trigger from def, validating what the source needs. existing is the stored
// row on update.
func (s *Service) applyInboundDefinition(ctx context.Context, userID string, def *reliantv1.TriggerDefinition, src *triggers.Source, trigger *core.Trigger, existing *core.Trigger) error {
	if strings.TrimSpace(def.Filter) != "" {
		if _, err := triggers.CompileFilter(def.Filter); err != nil {
			var fe *triggers.FilterError
			if errors.As(err, &fe) {
				return svcerr.InvalidArgument(fe.Error())
			}
			return svcerr.WrapOr(err, svcerr.Internal("could not compile the filter"))
		}
		trigger.Filter = strings.TrimSpace(def.Filter)
	}

	switch src.Kind {
	case core.TriggerKindWebhook:
		if s.deps.Intake == nil {
			return inboundUnavailable("webhook")
		}
		if src.HMAC && s.deps.Sealer == nil {
			return inboundUnavailable("signed webhook")
		}
		hasSecret := existing != nil && existing.Kind == core.TriggerKindWebhook && s.storedSecret(ctx, existing.ID)
		if src.HMAC && def.WebhookHmacSecret == nil && !hasSecret {
			return svcerr.InvalidArgument("webhook_hmac_secret is required to verify signatures")
		}
	case core.TriggerKindIntegration:
		if s.deps.Intake == nil || s.deps.Catalog == nil {
			return inboundUnavailable("integration")
		}
		if !s.deps.Catalog.HasInboundSource(src.Integration) {
			return svcerr.InvalidArgument(fmt.Sprintf("integration %q does not deliver events on this server", src.Integration))
		}
		if _, gated := s.deps.Access[src.Integration]; gated {
			// Access-gated: events route by the owner's own access, so a
			// connection is bound when one exists (and a named one must be
			// the caller's) but is not required — hosted GitHub's token is
			// delegated by control-plane and has no row here.
			connID, err := s.resolveOptionalTriggerConnection(ctx, userID, src.Integration, def.ConnectionId)
			if err != nil {
				return err
			}
			trigger.ConnectionID = connID
			break
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
func (s *Service) resolveTriggerConnection(ctx context.Context, userID, integration string, requested *string) (string, error) {
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
	if err != nil && !svcerr.IsNotFound(err) {
		return "", svcerr.WrapOr(err, svcerr.Internal("failed to get connection"))
	}
	if err != nil || conn == nil || conn.UserID != userID {
		if requested == nil || *requested == "" {
			msg := fmt.Sprintf("connect %s first: an integration trigger listens through one of your connections", integration)
			return "", tools.RefuseActivation(msg, svcerr.FailedPrecondition(msg))
		}
		return "", svcerr.NotFound("connection")
	}
	if conn.IntegrationID != integration {
		return "", svcerr.InvalidArgument(fmt.Sprintf("connection %q is for %s, not %s", conn.Name, conn.IntegrationID, integration))
	}
	if conn.Status == core.ConnectionStatusRevoked {
		return "", svcerr.NotFound("connection")
	}
	return conn.ID, nil
}

// resolveOptionalTriggerConnection is resolveTriggerConnection for an
// access-gated integration: a named connection must resolve exactly as
// there, but with none named, the caller's default is bound when it exists
// and nil is returned when it does not.
func (s *Service) resolveOptionalTriggerConnection(ctx context.Context, userID, integration string, requested *string) (*string, error) {
	if requested != nil && *requested != "" {
		id, err := s.resolveTriggerConnection(ctx, userID, integration, requested)
		if err != nil {
			return nil, err
		}
		return &id, nil
	}
	store := s.connections()
	if store == nil {
		return nil, nil
	}
	conn, err := store.DefaultConnection(ctx, userID, integration)
	if err != nil || conn == nil || conn.UserID != userID || conn.Status == core.ConnectionStatusRevoked {
		return nil, nil
	}
	return &conn.ID, nil
}

// refreshTriggerAccess re-reads the owner's access for an access-gated
// integration trigger that is (being made) enabled, so its first event
// routes, and so a trigger the owner cannot actually listen through is
// refused with the reason instead of silently never firing.
func (s *Service) refreshTriggerAccess(ctx context.Context, trigger *core.Trigger) error {
	if trigger == nil || trigger.Kind != core.TriggerKindIntegration || !trigger.Enabled {
		return nil
	}
	cfg, err := triggers.IntegrationConfigFor(trigger)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("could not read the trigger's integration config"))
	}
	access := s.deps.Access[cfg.Integration]
	if access == nil {
		return nil
	}
	if err := access.Refresh(ctx, trigger.UserID); err != nil {
		if access.IsPermanent(err) {
			// The reason is the access check's own sentence (not connected,
			// needs reconnecting): what the owner has to fix.
			return svcerr.WithCause(svcerr.FailedPrecondition(
				fmt.Sprintf("%s triggers need access to your %s repositories: %v", cfg.Integration, cfg.Integration, err)), err)
		}
		return svcerr.WrapOr(err, svcerr.Unavailable(fmt.Sprintf("could not read your %s access right now; try again", cfg.Integration)))
	}
	return nil
}

func (s *Service) storedSecret(ctx context.Context, triggerID string) bool {
	creds, err := s.deps.Repo.GetTriggerWebhookCredentials(ctx, triggerID)
	return err == nil && len(creds.SecretSealed) > 0
}

// writeWebhookCredentials issues a token for a new webhook trigger and
// stores its HMAC secret. It runs after the row exists, because the secret's
// sealing is bound to the trigger id.
func (s *Service) writeWebhookCredentials(ctx context.Context, trigger *core.Trigger, def *reliantv1.TriggerDefinition, issueToken bool) (*reliantv1.WebhookCredential, error) {
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
		return nil, svcerr.WrapOr(err, svcerr.Internal("could not read the webhook config"))
	}
	switch {
	case cfg.HMAC == nil:
		// Signatures off: drop any secret left from an earlier definition.
		if err := s.deps.Repo.SetTriggerWebhookSecret(ctx, trigger.ID, nil); err != nil {
			return nil, triggerDBError("clear webhook secret", err)
		}
	case def.WebhookHmacSecret != nil:
		if *def.WebhookHmacSecret == "" {
			return nil, svcerr.InvalidArgument("webhook_hmac_secret must not be empty")
		}
		// applyInboundDefinition already refused a signed webhook without
		// a sealer; this keeps the dereference safe on its own.
		if s.deps.Sealer == nil {
			return nil, inboundUnavailable("signed webhook")
		}
		sealed, err := s.deps.Sealer.Seal(ctx, vault.UserTenant(trigger.UserID),
			[]byte(*def.WebhookHmacSecret), triggers.WebhookSecretAAD(trigger.ID))
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("seal webhook secret failed"))
		}
		if err := s.deps.Repo.SetTriggerWebhookSecret(ctx, trigger.ID, sealed); err != nil {
			return nil, triggerDBError("set webhook secret", err)
		}
	}
	return cred, nil
}

func (s *Service) issueWebhookToken(ctx context.Context, triggerID string) (*reliantv1.WebhookCredential, error) {
	token, hash, err := triggers.NewWebhookToken()
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("could not issue a webhook token"))
	}
	if err := s.deps.Repo.SetTriggerWebhookTokenHash(ctx, triggerID, hash); err != nil {
		return nil, triggerDBError("set webhook token", err)
	}
	return &reliantv1.WebhookCredential{
		Token: token,
		Url:   triggers.WebhookURL(s.publicURL, triggerID) + "/" + token,
	}, nil
}

// RotateWebhookToken replaces a webhook trigger's token.
func (s *Service) RotateWebhookToken(
	ctx context.Context,
	req *connect.Request[reliantv1.RotateWebhookTokenRequest],
) (*connect.Response[reliantv1.RotateWebhookTokenResponse], error) {
	trigger, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if trigger.Kind != core.TriggerKindWebhook {
		return nil, svcerr.FailedPrecondition("only a webhook trigger has a token")
	}
	if s.managed(trigger) {
		// The new token goes to the sender, not to the caller. A paused
		// trigger has no receiver to give it to: its token is simply
		// replaced, and the next enable provisions a fresh one anyway.
		if trigger.Enabled {
			err = s.provisionAlerting(ctx, trigger)
		} else {
			_, err = s.issueWebhookToken(ctx, trigger.ID)
		}
		if err != nil {
			return nil, err
		}
		logging.InfoContext(ctx, "managed webhook trigger re-provisioned with a new token", "trigger_id", trigger.ID, "user_id", trigger.UserID)
		return connect.NewResponse(&reliantv1.RotateWebhookTokenResponse{Trigger: s.render(ctx, trigger)}), nil
	}
	cred, err := s.issueWebhookToken(ctx, trigger.ID)
	if err != nil {
		return nil, err
	}
	logging.InfoContext(ctx, "webhook trigger token rotated", "trigger_id", trigger.ID, "user_id", trigger.UserID)
	return connect.NewResponse(&reliantv1.RotateWebhookTokenResponse{
		Trigger: s.render(ctx, trigger),
		Webhook: cred,
	}), nil
}

// fireInbound is "run now" for an inbound trigger: a manual event with an
// empty payload except the marker, recorded and launched like any delivery.
func (s *Service) fireInbound(ctx context.Context, trigger *core.Trigger) (string, error) {
	if s.deps.Intake == nil {
		return "", inboundUnavailable(string(trigger.Kind))
	}
	requester := auth.MustGetUserID(ctx)
	res, err := s.deps.Intake.Accept(ctx, trigger, triggers.InboundEvent{
		Kind:      trigger.Kind.EventKind(),
		DedupeKey: trigger.ID + ":manual-" + uuid.NewString(),
		Payload:   map[string]any{"manual": true, "requested_by": requester},
		// "Run now" is sent by the signed-in person who pressed it, not by
		// the trigger's source.
		Sender: &core.TriggerSender{Kind: core.TriggerSenderKindUser, ID: requester, Verified: true},
	}, triggers.AcceptOptions{Manual: true})
	if err != nil {
		return "", svcerr.WrapOr(err, svcerr.Unavailable("record fire failed"))
	}
	return triggers.EventFireWorkflowID(res.EventID), nil
}
