package trigger

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"go.temporal.io/sdk/client"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the trigger service. internal/grpc/server.go
// constructs it today (and the worker, for list_triggers); forge's
// composition root takes over in P3c (research/forge-migration/PLAN.md). The
// rpc methods on *Service are where this service's logic lives; see the
// forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo stores triggers, their firings and webhook credentials, and reads
	// the projects, chats, daemons, workflows and presets a trigger names. It
	// is the whole db.Repository: the handlers call over 25 of its methods,
	// hand it to launch, daemonchoice and triggers, and reach the connection
	// store through db.Repo.Connections by assertion.
	Repo db.Repository

	// Syncer converges a trigger's Temporal Schedule, and Fires starts a
	// manual fire. TemporalBackend builds both over a Temporal client.
	// Without them (a deployment with no Temporal) a schedule trigger is
	// refused rather than stored unable to fire, and FireTrigger answers
	// Unavailable.
	// forge:optional-dep
	Syncer Syncer
	// forge:optional-dep
	Fires FireStarter

	// ControlPlane mints delegated automation credentials: while a user has
	// an enabled trigger for a daemon, a daemon-bound daemon:resume token is
	// held so an unattended fire can wake exactly that daemon. Without it
	// (self-hosted) nothing is minted.
	// forge:optional-dep
	ControlPlane AutomationTokens

	// The inbound kinds (webhook, integration and workflow-event triggers).
	// Each is optional; a kind whose dependency is missing is rejected with
	// Unavailable at write time rather than stored unable to fire.
	//
	// PublicURL is this server's externally reachable base, used to render
	// webhook URLs; empty renders a bare path.
	PublicURL string
	// forge:optional-dep
	Sealer WebhookSealer
	// forge:optional-dep
	Catalog IntegrationCatalog
	// forge:optional-dep
	Intake EventIntake
	// Access names the integrations whose events are access-gated. A
	// trigger of one needs no saved connection (its token may be
	// delegated), and writing or re-enabling it refreshes the owner's access
	// so the first event routes. See webhook.Event.ResourceKey.
	// forge:optional-dep
	Access AccessGates

	// Senders looks people up for "Only from" (ResolveTriggerSenders). An
	// integration without a directory answers Unavailable.
	// forge:optional-dep
	Senders SenderDirectories
}

// AccessGates names the integrations whose events are access-gated.
type AccessGates interface {
	// Gate returns the access refresher of integration, and whether its
	// events are access-gated at all.
	Gate(integration string) (IntegrationAccess, bool)
}

// AccessByIntegration is AccessGates over a map keyed by integration id.
type AccessByIntegration map[string]IntegrationAccess

// Gate implements AccessGates.
func (m AccessByIntegration) Gate(integration string) (IntegrationAccess, bool) {
	access, ok := m[integration]
	return access, ok
}

// SenderDirectories finds the SenderDirectory of an integration.
type SenderDirectories interface {
	Directory(integration string) (SenderDirectory, bool)
}

// SendersByIntegration is SenderDirectories over a map keyed by integration
// id.
type SendersByIntegration map[string]SenderDirectory

// Directory implements SenderDirectories.
func (m SendersByIntegration) Directory(integration string) (SenderDirectory, bool) {
	dir, ok := m[integration]
	return dir, ok
}

// Syncer is the schedule backend the handlers converge onto. Declared here,
// at the consumer, so the handlers can be tested without Temporal.
type Syncer interface {
	Sync(ctx context.Context, triggerID string) error
	Delete(ctx context.Context, triggerID string) error
	NextFireAt(ctx context.Context, triggerID string) (*time.Time, error)
}

// FireStarter starts a manual fire. Separate from Syncer because it needs a
// workflow client rather than a schedule client, and because a deployment
// could in principle converge schedules without allowing run-now.
type FireStarter interface {
	StartManualFire(ctx context.Context, triggerID string) (string, error)
}

// AutomationTokens mints and revokes the delegated daemon:resume tokens of
// automationGrants. controlplane.Client implements it.
type AutomationTokens interface {
	MintDaemonResumeToken(ctx context.Context, jwt, daemonID, name string) (controlplane.DaemonResumeToken, error)
	RevokeDaemonResumeTokens(ctx context.Context, jwt, daemonID string) error
}

// TemporalBackend is the Syncer and FireStarter over a Temporal client:
// schedules converge onto its Schedules and fires start on taskQueue, and an
// integration polled reports true for gets a poll schedule (nil polls none).
//
// A nil client, a deployment without Temporal, returns nil interfaces. That
// is the point of building them here: a nil *triggers.Backend assigned to
// Deps.Syncer is a non-nil interface holding a nil pointer, which passes
// the handlers' nil checks and panics on the first write.
func TemporalBackend(temporalClient client.Client, repo db.Repository, taskQueue string, polled func(integration string) bool) (Syncer, FireStarter) {
	if temporalClient == nil {
		return nil, nil
	}
	backend := triggers.NewBackend(temporalClient.ScheduleClient(), temporalClient, repo, taskQueue)
	if polled != nil {
		backend.WithPolledIntegrations(polled)
	}
	return backend, backend
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("trigger: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("trigger: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("trigger: Deps.Repo is required")
	}
	return nil
}

// Service implements the trigger Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedTriggerServiceHandler
	deps Deps

	// grants holds delegated automation credentials; nil (no control
	// plane) makes it a no-op.
	grants *automationGrants
}

// New creates a new trigger service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	// The typed-nil guard TemporalBackend keeps, for a composition that
	// builds the backend itself: a nil *triggers.Backend means no Temporal.
	if b, ok := deps.Syncer.(*triggers.Backend); ok && b == nil {
		deps.Syncer = nil
	}
	if b, ok := deps.Fires.(*triggers.Backend); ok && b == nil {
		deps.Fires = nil
	}
	s := &Service{deps: deps}
	if deps.ControlPlane != nil {
		s.grants = &automationGrants{client: deps.ControlPlane, keys: deps.Repo, triggers: deps.Repo}
	}
	return s, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "trigger"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewTriggerServiceHandler(s, opts...)
	mux.Handle(path, handler)
}

// RegisterHTTP registers plain HTTP handlers (callbacks, REST endpoints)
// outside Connect RPC. The stack provides recovery/logging/audit; auth is
// per-route. Override to add endpoints — forge.yaml webhooks are wired
// automatically (do not call RegisterWebhookRoutes here). See the forge
// `api` skill.
func (s *Service) RegisterHTTP(_ *http.ServeMux, _ func(http.Handler) http.Handler) {
	// No HTTP endpoints by default. Example:
	//   mux.Handle("POST /api/callbacks/oauth", stack(http.HandlerFunc(s.HandleOAuthCallback)))
}

// Ensure the generated protobuf package import remains referenced even before methods exist.
var _ = pb.File_reliant_v1_trigger_proto
