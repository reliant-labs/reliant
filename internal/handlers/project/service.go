package project

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/projectaccess"

	"connectrpc.com/connect"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the project service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo stores projects and what hangs off them: their daemons, repos,
	// main worktree, seeded config row and prompts.
	Repo Repository
	// Lookups finds a user's project by path or remote within an org.
	Lookups ProjectLookups
	// Installs lists the checkouts a machine holds.
	Installs DaemonInstalls
	// Detachments records and reads a project's detachments from machines.
	Detachments Detachments
	// Projects reads a project the caller may write (owner and org decided
	// together: projectaccess).
	Projects projectaccess.Service
	// Purges schedules, restores and purges a project (projectpurge).
	Purges ProjectPurges
	// Router runs every filesystem and git operation on the caller's
	// daemon, which owns the filesystem. The API server always has one; in
	// a composition without one, the daemon-backed steps fail through their
	// existing error paths and the database-only rpcs still work.
	// forge:optional-dep
	Router Router
	// ControlPlane clones a repository onto a managed daemon for
	// CreateProjectFromRepo. Nil in self-hosted deployments, where that rpc
	// reports that cloning needs a cloud account: the user's GitHub token
	// lives in the control plane and nowhere else, and defaulting to
	// localhost would fail obscurely against a control plane that is not
	// there.
	// forge:optional-dep
	ControlPlane CloneDispatcher
	// Apps reads the org's hosted apps for ListProjectsWithApps, as the
	// caller. Nil (self-hosted: no control plane) lists projects with their
	// apps unlinked and the hosted apps UNAVAILABLE.
	// forge:optional-dep
	Apps HostedApps
	// Orgs says which organization a create acts in and verifies the org a
	// list names (internal/orgs). Nil, or a nil *orgs.Resolver, is a
	// deployment with no orgs: projects are created with none and lists
	// ignore org_id.
	// forge:optional-dep
	Orgs OrgResolver
	// OrgRollout is the deployment's org rollout gates: MoveProjectToOrg is
	// refused unless orgs.project_moves_enabled is on (MovesAllowed). Nil is
	// every gate off.
	// forge:optional-dep
	OrgRollout *configv1.OrgsConfig
	// Ledger is a move's ledger and preview (MoveLedger). Required: every
	// project move rpc reads it.
	Ledger MoveLedger
	// Shared lists what others shared with the caller's org for
	// ListProjectsWithApps (SharedProjects).
	Shared SharedProjects
	// Moves starts a move's saga (orgmove.ProjectOrgMoveWorkflow). Nil — no
	// Temporal — refuses MoveProjectToOrg.
	// forge:optional-dep
	Moves MoveStarter
	// MoveFacts is control-plane's half of PreviewProjectOrgMove, signed as
	// the internal service. Nil (self-hosted, or no internal-service
	// identity) previews those facts as unknown.
	// forge:optional-dep
	MoveFacts MoveFacts
	// Schedules pauses and resumes the Temporal Schedules of the triggers a
	// project delete pauses and a restore resumes, and removes those of a
	// purged project's triggers; a sync that fails is retried in the
	// background (trigger_schedule_retry.go). Nil converges only the rows: a
	// paused trigger's fires skip on its row until the startup sync
	// converges it.
	// forge:optional-dep
	Schedules TriggerSchedules
	// Now is the clock a delete's restore window starts from; nil is
	// time.Now.
	// forge:optional-dep
	Now func() time.Time
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("project: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("project: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("project: Deps.Repo is required")
	}
	if d.Lookups == nil {
		return fmt.Errorf("project: Deps.Lookups is required")
	}
	if d.Installs == nil {
		return fmt.Errorf("project: Deps.Installs is required")
	}
	if d.Detachments == nil {
		return fmt.Errorf("project: Deps.Detachments is required")
	}
	if d.Projects == nil {
		return fmt.Errorf("project: Deps.Projects is required")
	}
	if d.Purges == nil {
		return fmt.Errorf("project: Deps.Purges is required")
	}
	if d.Ledger == nil {
		return fmt.Errorf("project: Deps.Ledger is required")
	}
	if d.Shared == nil {
		return fmt.Errorf("project: Deps.Shared is required")
	}
	return nil
}

// Service implements the project Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedProjectServiceHandler
	deps Deps
	// failedCreateCleanups holds the daemon ids whose connect-time
	// failed-create cleanup is running (failed_create_cleanup.go), so a
	// machine that reconnects in a loop runs one at a time.
	failedCreateCleanups sync.Map
	// purges tracks the immediate purges whose batches run after their
	// request returned (rpc_purge_project_now.go startPurge).
	purges sync.WaitGroup
	// scheduleRetries retries the trigger schedules a delete, restore or
	// purge could not converge (trigger_schedule_retry.go).
	scheduleRetries scheduleRetries
}

// New creates a new project service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// now is Deps.Now, or time.Now when it is nil.
func (s *Service) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "project"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewProjectServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_project_proto
