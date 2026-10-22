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
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the trigger service. The composition graph
// constructs it (internal/app/compose.go), and the worker root builds its own
// for list_triggers (internal/app/providers_worker.go). The rpc methods on
// *Service are where this service's logic lives; see the forge `api` /
// `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo stores triggers, their firings and webhook credentials, and reads
	// the projects, chats, daemons, workflows and presets a trigger names
	// (deps.go).
	Repo Repository

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
	// Access holds the access refresher of each integration whose events
	// are access-gated, keyed by integration id. A trigger of one needs no
	// saved connection (its token may be delegated), and writing or
	// re-enabling it refreshes the owner's access so the first event
	// routes. See webhook.Event.ResourceKey. Nil gates none.
	// forge:optional-dep
	Access map[string]IntegrationAccess

	// Senders looks people up for "Only from" (ResolveTriggerSenders),
	// keyed by integration id. An integration without a directory answers
	// Unavailable.
	// forge:optional-dep
	Senders map[string]SenderDirectory

	// Options checks, at enable time, that the values picked for an
	// integration input can be reached (see OptionsChecker). Without it
	// enabling checks shapes only.
	// forge:optional-dep
	Options OptionsChecker

	// Alerting wires the control plane's alert receiver of a webhook
	// trigger declared `source: alerting`: provisioned on enable (with a
	// token the user never sees), removed on pause and delete. Nil
	// (self-hosted, no control plane) leaves such a trigger a plain webhook
	// whose token Enable shows once.
	// forge:optional-dep
	Alerting AlertingProvisioner
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
// automationGrants. controlplane.Service implements it.
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
// the handlers' nil checks and panics on the first write. New does not
// unwrap one, so every composition builds Syncer and Fires through here.
func TemporalBackend(temporalClient client.Client, repo triggers.Repo, taskQueue string, polled func(integration string) bool) (Syncer, FireStarter) {
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
