// Copyright (c) 2026 Reliant Labs

package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/credentials/connectionvault"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/dblock"
	"github.com/reliant-labs/reliant/internal/featuregate"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/analytics"
	"github.com/reliant-labs/reliant/internal/gtm/assetstore"
	"github.com/reliant-labs/reliant/internal/gtm/email"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/store"
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
// Both hold the GTM assets bucket (GTM_ASSETS_*, GtmAssets): the api for the
// upload and download rpcs, the worker for attach_gtm_asset. With no bucket
// configured, assets are off and those answer unavailable. Only the api runs
// the stale-PENDING sweep (GtmAssetSweep), on one replica at a time.
//
// Only the worker reads chat attachments (gtm.Deps.Attachments, for
// attach_gtm_asset), and only with GTM_ATTACH_FROM_CHAT_ENABLED: then an
// attachment is taken only when it is the actor's own AND stamped with the
// actor's org (gtmAttachments). Off, the tool answers unavailable.
//
// The settings are on ServerProcessConfig (GtmConfig, GtmAssetsConfig), read
// here as compose.go hands them over, so only the api and the worker, whose
// graphs read them, are given the GTM_* variables; the gateway never is.

// FeatureGateDeps is the allowlist the gate reads and the membership it asks.
type FeatureGateDeps struct {
	// Gtm is ServerProcessConfig.gtm (GTM_ALLOWED_ORG_IDS); nil is GTM off.
	Gtm *configv1.GtmConfig
	// Logger receives whether GTM is on; nil is slog.Default().
	Logger *slog.Logger
	// Membership is the org resolver authz decides org resources with; nil
	// when the deployment has no orgs (no control plane).
	Membership *orgs.Resolver
}

// NewFeatureGate is the deployment's featuregate.Gate (featuregate.NewStatic):
// GTM is on for the orgs in GTM_ALLOWED_ORG_IDS, for their live members only.
// It fails closed: no list, an empty one, or no orgs is a Gate that allows
// nothing, and the list is read once (a change takes a restart).
func NewFeatureGate(d FeatureGateDeps) (featuregate.Gate, error) {
	cfg := d.Gtm
	log := orDefaultLogger(d.Logger)
	// A nil *orgs.Resolver must stay a nil Membership: NewStatic denies all
	// without one.
	var membership featuregate.Membership
	if d.Membership != nil {
		membership = d.Membership
	}
	switch n := len(featuregate.ParseOrgIDs(cfg.GetAllowedOrgIds())); {
	case n == 0:
		log.Info("gtm: off: GTM_ALLOWED_ORG_IDS names no organization")
	case membership == nil:
		log.Warn("gtm: off: GTM_ALLOWED_ORG_IDS is set but this deployment has no organizations (no control plane)")
	default:
		log.Info("gtm: on for the organizations in GTM_ALLOWED_ORG_IDS", "orgs", n)
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
	Assets          *GtmAssets
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
		Assets:      d.Assets.store(),
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
	// Gtm is ServerProcessConfig.gtm: the analytics token, and whether the
	// agent may copy chat attachments (attach_from_chat_enabled).
	Gtm    *configv1.GtmConfig
	Logger *slog.Logger
	Assets *GtmAssets
}

// NewWorkerGtmDomain builds the worker's domain. Analytics is off without a
// control plane or with a URL the client refuses, and email is off if the
// Gmail sender cannot be built: each is then reported unavailable by the
// tool that needs it, and the rest of GTM still works.
//
//forge:root-only worker
func NewWorkerGtmDomain(d WorkerGtmDomainDeps) (*WorkerGtmDomain, error) {
	log := orDefaultLogger(d.Logger)
	svc := gtmsvc.NewInstrumented(gtmsvc.Deps{
		Store:       gtmsvc.NewStore(d.DB.ORM()),
		Gate:        d.Gate,
		Analytics:   gtmAnalytics(d.ControlPlane, d.Gtm, log),
		Email:       gtmEmail(d.Credentials, d.WriteLedger, log),
		Connections: gtmConnections{store: d.ConnectionVault},
		Assets:      d.Assets.store(),
		Attachments: gtmChatAttachments(d.Gtm, d.DB, log),
	})
	return &WorkerGtmDomain{
		Service: svc,
		Email:   email.NewApprover(svc, email.NewConnectionAccounts(d.ConnectionVault)),
	}, nil
}

// GtmAssets is the GTM assets bucket (internal/gtm/assetstore over forge
// blobstore), shared by the process's GTM domain and its sweep.
type GtmAssets struct {
	// Store is nil when no bucket is configured: assets are off.
	Store gtmsvc.AssetStore
}

// GtmAssetsDeps is the config the bucket is read from.
type GtmAssetsDeps struct {
	// Assets is ServerProcessConfig.gtm_assets (GTM_ASSETS_*); nil is
	// assets off.
	Assets *configv1.GtmAssetsConfig
	// Logger receives whether assets are on; nil is slog.Default().
	Logger *slog.Logger
}

// NewGtmAssets opens the bucket. No GTM_ASSETS_BUCKET is assets off, never
// an error; a bucket without its endpoint or credentials fails boot (the
// config's own validation refuses it at load too).
func NewGtmAssets(d GtmAssetsDeps) (*GtmAssets, error) {
	blobs, err := assetstore.StoreFromConfig(d.Assets)
	if err != nil {
		return nil, err
	}
	if blobs == nil {
		orDefaultLogger(d.Logger).Info("gtm: assets off: GTM_ASSETS_BUCKET is not set")
	}
	return &GtmAssets{Store: assetstore.New(blobs)}, nil
}

// store is a's AssetStore, nil (assets off) for a nil a.
func (a *GtmAssets) store() gtmsvc.AssetStore {
	if a == nil {
		return nil
	}
	return a.Store
}

// gtmAssetSweepInterval is how often the api looks for stale PENDING assets.
const gtmAssetSweepInterval = time.Hour

// GtmAssetSweep deletes GTM assets left PENDING past
// assetstore.StalePendingAge, whose org never came back to finish or retry
// the upload. It runs in the api root only.
//
// The lock is per pass, not a lease: every api replica runs its own hourly
// ticker, and a pass runs only while its replica holds dblock.GtmAssetSweep,
// so two passes never overlap. A replica whose tick finds the lock free runs
// its pass even if another replica swept minutes ago, so with N replicas the
// sweep may run up to N times an hour. That is safe: a pass is idempotent
// (it deletes only rows still PENDING and older than StalePendingAge, then
// their objects), so a repeat finds nothing left to do.
type GtmAssetSweep struct {
	sweeper *assetstore.Sweeper
	locker  dblock.Service
}

// GtmAssetSweepDeps is the database the asset rows live in and the bucket
// their objects are in.
type GtmAssetSweepDeps struct {
	DB     *db.Repo
	Assets *GtmAssets
}

// NewGtmAssetSweep is the sweep, or nothing (nil) with assets off.
//
//forge:root-only api
func NewGtmAssetSweep(d GtmAssetSweepDeps) (*GtmAssetSweep, error) {
	if d.Assets.store() == nil {
		return nil, nil
	}
	locker, err := dblock.NewInstrumented(dblock.Deps{Sessions: d.DB.SessionPool()})
	if err != nil {
		return nil, err
	}
	return &GtmAssetSweep{
		sweeper: assetstore.NewSweeper(assetstore.SweeperDeps{DB: d.DB.ORM(), Assets: d.Assets.Store}),
		locker:  locker,
	}, nil
}

// Run sweeps hourly until ctx ends.
func (s *GtmAssetSweep) Run(ctx context.Context) error {
	ticker := time.NewTicker(gtmAssetSweepInterval)
	defer ticker.Stop()
	for {
		s.pass(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *GtmAssetSweep) pass(ctx context.Context) {
	release, ok, err := s.locker.TryLock(ctx, dblock.GtmAssetSweep)
	if err != nil {
		logging.WarnContext(ctx, "gtm: stale asset sweep: taking the lock failed", "error", err)
		return
	}
	if !ok {
		return
	}
	defer release()
	res, err := s.sweeper.SweepStalePending(ctx, time.Now())
	switch {
	case err != nil && ctx.Err() == nil:
		logging.WarnContext(ctx, "gtm: stale asset sweep pass failed", "deleted", res.Deleted, "orphaned", res.Orphaned, "error", err)
	case res.Deleted > 0:
		logging.InfoContext(ctx, "gtm: stale asset sweep pass", "deleted", res.Deleted, "orphaned", res.Orphaned)
	}
}

// gtmAnalytics is the product-analytics read over the control plane's
// ObservabilityService, authenticated by GTM_ANALYTICS_TOKEN; nil (the read
// is unavailable) without a control plane or with a URL it refuses. An unset
// token is refused per query by the client.
func gtmAnalytics(cp *configv1.ControlPlaneConfig, cfg *configv1.GtmConfig, log *slog.Logger) gtmsvc.Analytics {
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
	// One token for the whole analytics read path, the panel query and the
	// environment lookup alike (obs:read, deploy:read); nothing else sends it.
	token := func() string { return cfg.GetAnalyticsToken() }
	// The environment filter resolves a name to the org's environment ids
	// through the control plane's DeployService. Without it a query that
	// names an environment is refused, never read across every one.
	var envs analytics.EnvResolver
	if deploy, err := analytics.NewDeployClient(endpoint.URL); err != nil {
		log.Warn("gtm: product analytics environment filter unavailable", "error", err)
	} else {
		envs = analytics.NewEnvResolver(analytics.EnvResolverDeps{CP: deploy, Token: token, Log: log})
	}
	return analytics.NewInstrumented(analytics.Deps{
		CP:    client,
		Token: token,
		Envs:  envs,
		Log:   log,
	})
}

// gtmEmail is the Gmail sender (internal/gtm/email): message.send through
// the integration runner, recorded in the write ledger as the run's write,
// with the run owner's own Gmail connection. nil (sending is unavailable)
// when it cannot be built, and without a write ledger: a send must be
// recorded as the run's write, so none goes out unrecorded.
func gtmEmail(creds *WorkerIntegrationCredentials, ledger writeledger.Service, log *slog.Logger) gtmsvc.Email {
	if creds == nil || creds.Runs == nil {
		log.Warn("gtm: email sending unavailable: no run records")
		return nil
	}
	if ledger == nil {
		log.Warn("gtm: email sending unavailable: no write ledger to record sends in")
		return nil
	}
	runner := ledger.Wrap(httpaction.NewRunner(netguard.New()))
	sender, err := email.NewGmail(email.Deps{Runner: runner, Credentials: creds.Source, Runs: creds.Runs})
	if err != nil {
		log.Error("gtm: email sending unavailable", "error", err)
		return nil
	}
	return sender
}

// gtmChatAttachments is gtm.Attachments over the attachments table, or nil
// (attach_gtm_asset unavailable) unless GTM_ATTACH_FROM_CHAT_ENABLED is on.
func gtmChatAttachments(cfg *configv1.GtmConfig, repo *db.Repo, log *slog.Logger) gtmsvc.Attachments {
	if !cfg.GetAttachFromChatEnabled() || repo == nil {
		log.Info("gtm: copying chat attachments into assets is off (GTM_ATTACH_FROM_CHAT_ENABLED)")
		return nil
	}
	return gtmAttachments{db: repo.ORM()}
}

// gtmAttachments is gtm.Attachments over the attachments table: an
// attachment opens only for its owner and only when it is stamped with the
// actor's org (store.GetAttachmentForUserInOrg). Anything else (another
// user's, another org's, one stamped with no org, one that does not exist,
// or a file reference with no stored bytes) is the bare gtm.ErrNotFound, so
// an agent in one org cannot pull a file its owner attached in another.
type gtmAttachments struct{ db orm.Context }

func (a gtmAttachments) Open(ctx context.Context, actor gtmsvc.Actor, attachmentID string) (gtmsvc.AttachmentFile, io.ReadCloser, error) {
	att, err := store.GetAttachmentForUserInOrg(ctx, a.db, actor.UserID, actor.OrgID, attachmentID)
	switch {
	case err != nil:
		return gtmsvc.AttachmentFile{}, nil, err
	case att == nil || att.Content == nil:
		return gtmsvc.AttachmentFile{}, nil, gtmsvc.ErrNotFound
	}
	return gtmsvc.AttachmentFile{Filename: att.Filename, ContentType: att.MimeType, Size: int64(len(att.Content))},
		io.NopCloser(bytes.NewReader(att.Content)), nil
}

// gtmConnections is gtm.Connections over the saved connections: whether the
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

// orDefaultLogger is log, or slog.Default() for nil.
func orDefaultLogger(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.Default()
	}
	return log
}

func connectionMissing(err error) bool {
	return err != nil && (errors.Is(err, core.ErrConnectionNotFound) || svcerr.IsNotFound(err))
}
