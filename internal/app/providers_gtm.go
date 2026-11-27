// Copyright (c) 2026 Reliant Labs

package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/reliant-labs/forge/pkg/svcerr"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/credentials/connectionvault"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/featuregate"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/analytics"
	"github.com/reliant-labs/reliant/internal/gtm/email"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/pkg/config"
)

// GTM's providers: the feature gate every GTM surface asks, and the GTM
// domain (internal/gtm) each root serves it from.
//
// One Gate per process answers nav (GetGtmAccess), every GtmService rpc, the
// agent's GTM tools (call_llm's entitlements, the domain's own re-check),
// CatalogService.ListTools and the trigger activation cards, so they cannot
// disagree. It is GTM_ALLOWED_ORG_IDS over the org resolver authz decides
// with; an empty list, or a deployment with no orgs, is a Gate that allows
// nothing.
//
// The api-server's domain serves GtmService and sends nothing: no rpc sends
// an email or reads analytics. The worker's domain is the agent's: it reads
// product analytics through the control plane and sends approved emails
// through the run owner's Gmail.
//
// Neither has chat attachments (gtmsvc.Deps.Attachments) or asset storage
// (gtmsvc.Deps.Assets) yet, so attach_gtm_asset and the asset upload and
// download rpcs answer unavailable. An attachment row names its owner but no
// org or chat, and the domain must not copy one user's file from another
// org's chat into this org's workspace.

// FeatureGateDeps is the allowlist the gate reads and the membership it asks.
type FeatureGateDeps struct {
	// Config holds GtmConfig (GTM_ALLOWED_ORG_IDS).
	Config *config.Config
	// Membership is the org resolver authz decides org resources with; nil
	// when the deployment has no orgs (no control plane).
	Membership *orgs.Resolver
}

// NewFeatureGate is the deployment's featuregate.Gate (featuregate.NewStatic):
// GTM is on for the orgs in GTM_ALLOWED_ORG_IDS, for their live members only.
// It fails closed: no list, an empty one, or no orgs is a Gate that allows
// nothing, and the list is read once (a change takes a restart).
func NewFeatureGate(d FeatureGateDeps) (featuregate.Gate, error) {
	cfg := d.Config.GetGtm()
	// A nil *orgs.Resolver must stay a nil Membership: NewStatic denies all
	// without one.
	var membership featuregate.Membership
	if d.Membership != nil {
		membership = d.Membership
	}
	switch n := len(featuregate.ParseOrgIDs(cfg.GetAllowedOrgIds())); {
	case n == 0:
		slog.Default().Info("gtm: off: GTM_ALLOWED_ORG_IDS names no organization")
	case membership == nil:
		slog.Default().Warn("gtm: off: GTM_ALLOWED_ORG_IDS is set but this deployment has no organizations (no control plane)")
	default:
		slog.Default().Info("gtm: on for the organizations in GTM_ALLOWED_ORG_IDS", "orgs", n)
	}
	return featuregate.NewStatic(cfg, membership), nil
}

// GtmDomain is the api-server's GTM domain, which GtmService serves.
type GtmDomain struct{ gtmsvc.Service }

// GtmDomainDeps is the database, the gate, and the saved connections a
// channel's connection_state is read from.
type GtmDomainDeps struct {
	DB              *db.Repo
	Gate            featuregate.Gate
	ConnectionVault connectionvault.Service
}

// NewGtmDomain builds the api-server's domain: no email sender and no
// analytics, which only the agent's tools use.
//
//forge:root-only api
func NewGtmDomain(d GtmDomainDeps) (*GtmDomain, error) {
	return &GtmDomain{gtmsvc.NewInstrumented(gtmsvc.Deps{
		Store:       gtmsvc.NewStore(d.DB.ORM()),
		Gate:        d.Gate,
		Connections: gtmConnections{store: d.ConnectionVault},
	})}, nil
}

// WorkerGtmDomain is the worker's GTM domain, which the agent's GTM tools
// call, and the approver send_gtm_email sends through.
type WorkerGtmDomain struct {
	gtmsvc.Service
	// Email is send_gtm_email's approver over Service: the card, and the
	// send of exactly what the owner approved, from their Gmail.
	Email *email.Approver
}

// WorkerGtmDomainDeps is what the agent's GTM reaches: the database and
// gate, the saved connections, the credential source and write ledger a
// Gmail send goes through, and the control plane analytics is read from.
type WorkerGtmDomainDeps struct {
	DB              *db.Repo
	Gate            featuregate.Gate
	ConnectionVault connectionvault.Service
	Credentials     *WorkerIntegrationCredentials
	WriteLedger     writeledger.Service
	ControlPlane    *configv1.ControlPlaneConfig
	Config          *config.Config
	Logger          *slog.Logger
}

// NewWorkerGtmDomain builds the worker's domain. Analytics is off without a
// control plane or with a URL the client refuses, and email is off if the
// Gmail sender cannot be built: each is then reported unavailable by the
// tool that needs it, and the rest of GTM still works.
//
//forge:root-only worker
func NewWorkerGtmDomain(d WorkerGtmDomainDeps) (*WorkerGtmDomain, error) {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	svc := gtmsvc.NewInstrumented(gtmsvc.Deps{
		Store:       gtmsvc.NewStore(d.DB.ORM()),
		Gate:        d.Gate,
		Analytics:   gtmAnalytics(d.ControlPlane, d.Config, log),
		Email:       gtmEmail(d.Credentials, d.WriteLedger, log),
		Connections: gtmConnections{store: d.ConnectionVault},
	})
	return &WorkerGtmDomain{
		Service: svc,
		Email:   email.NewApprover(svc, email.NewConnectionAccounts(d.ConnectionVault)),
	}, nil
}

// gtmAnalytics is the product-analytics read over the control plane's
// ObservabilityService, authenticated by GTM_ANALYTICS_TOKEN; nil (the read
// is unavailable) without a control plane or with a URL it refuses. An unset
// token is refused per query by the client.
func gtmAnalytics(cp *configv1.ControlPlaneConfig, cfg *config.Config, log *slog.Logger) gtmsvc.Analytics {
	endpoint := ControlPlaneEndpoint(cp)
	if !endpoint.Hosted() {
		log.Info("gtm: product analytics unavailable: no control plane")
		return nil
	}
	client, err := analytics.NewObservabilityClient(endpoint.URL)
	if err != nil {
		log.Warn("gtm: product analytics unavailable: the control-plane URL was refused", "error", err)
		return nil
	}
	return analytics.NewInstrumented(analytics.Deps{
		CP:    client,
		Token: func() string { return cfg.GetGtm().GetAnalyticsToken() },
		Log:   log,
	})
}

// gtmEmail is the Gmail sender (internal/gtm/email): message.send through
// the integration runner, recorded in the write ledger as the run's write,
// with the run owner's own Gmail connection. nil (sending is unavailable)
// when it cannot be built.
func gtmEmail(creds *WorkerIntegrationCredentials, ledger writeledger.Service, log *slog.Logger) gtmsvc.Email {
	if creds == nil || creds.Runs == nil {
		log.Warn("gtm: email sending unavailable: no run records")
		return nil
	}
	var runner writeledger.Runner = httpaction.NewRunner(netguard.New())
	if ledger != nil {
		runner = ledger.Wrap(runner)
	}
	sender, err := email.NewGmail(email.Deps{Runner: runner, Credentials: creds.Source, Runs: creds.Runs})
	if err != nil {
		log.Error("gtm: email sending unavailable", "error", err)
		return nil
	}
	return sender
}

// gtmConnections is gtmsvc.Connections over the saved connections: whether the
// actor has a live connection of an integration, for an INTEGRATED channel's
// connection_state. Display only, never authorization. It reads as
// email.NewConnectionAccounts (and connections.Resolver) do: a named
// connection is the actor's own, else their org's; none named is the actor's
// default connection of the integration.
type gtmConnections struct{ store connectionvault.Service }

func (c gtmConnections) State(ctx context.Context, actor gtmsvc.Actor, integrationKind, connectionID string) (gtmsvc.ConnectionState, error) {
	if c.store == nil || actor.UserID == "" || integrationKind == "" {
		return gtmsvc.ConnectionUnknown, nil
	}
	var conn *core.Connection
	var err error
	if connectionID != "" {
		conn, err = c.store.GetConnection(ctx, actor.UserID, connectionID)
		if connectionMissing(err) && actor.OrgID != "" {
			conn, err = c.store.GetOwnedConnection(ctx, core.OrgConnectionOwner(actor.OrgID), connectionID)
		}
	} else {
		conn, err = c.store.DefaultConnection(ctx, actor.UserID, integrationKind)
	}
	switch {
	case connectionMissing(err):
		return gtmsvc.ConnectionNotConnected, nil
	case err != nil:
		return gtmsvc.ConnectionUnknown, err
	case conn == nil || conn.IntegrationID != integrationKind || conn.Status != core.ConnectionStatusActive:
		return gtmsvc.ConnectionNotConnected, nil
	}
	return gtmsvc.ConnectionConnected, nil
}

func connectionMissing(err error) bool {
	return err != nil && (errors.Is(err, core.ErrConnectionNotFound) || svcerr.IsNotFound(err))
}
