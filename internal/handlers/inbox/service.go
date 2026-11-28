package inbox

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the inbox service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads what waits on the caller (pending rows, automation
	// health, machines and their storage) and records dismissals.
	Repo Repository
	// ProjectRows decides an item about a project (a recovery, an
	// automation) through authz: one of an org the caller has left is not
	// listed.
	ProjectRows ProjectReader
	// Chats reads the chat an item or a machine process names (deps.go).
	Chats Chats
	// Machines reads a machine by id; the handler decides whose it is.
	Machines DaemonReader
	// OrgKeys lists the org keys a provider rejected (credentials/orgaccounts).
	OrgKeys OrgKeys
	// Items, Firings and Notices are the Inbox's own reads (deps.go).
	Items   Items
	Firings Firings
	Notices Notices
	// Registrations reads the registrations of the triggers the Inbox judges.
	Registrations Registrations
	// Recoveries lists the caller's worktree recoveries.
	Recoveries Recoveries
	// Preferences reads the person's Inbox filters (inbox.show_*). nil reads
	// none: every filter is at its default.
	// forge:optional-dep
	Preferences Preferences
	// Marks lists the caller's worktrees marked for reclaim and records keep /
	// reclaim now. store.WorktreeExtStore satisfies it.
	Marks worktreesweep.MarkDecisionStore
	// Storage is the machines' storage view, forget and clean-up (deps.go).
	Storage Storage
	// Sweeper runs the confirmed storage clean-up on a machine's daemon.
	// nil when no daemon router is wired; CleanupStorage then reports
	// unavailable.
	// forge:optional-dep
	Sweeper Sweeper
	// Orgs scopes ListInbox to a named org (org_id). nil (or a nil
	// *orgs.Resolver) is a deployment with no orgs: org_id is ignored.
	// forge:optional-dep
	Orgs OrgResolver
	// MyOrgs lists the caller's live org memberships, with their role in each, from the control
	// plane, as the caller: who is told of an org's rejected API keys. nil (self-hosted, no
	// control plane) lists none.
	// forge:optional-dep
	MyOrgs OrgLister
	// Members counts an org's live members: which of the caller's orgs are
	// teams, where their automations on their own accounts are listed
	// (org-provider-accounts §11.3). nil lists none.
	// forge:optional-dep
	Members OrgMemberCounter
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("inbox: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("inbox: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("inbox: Deps.Repo is required")
	}
	if d.Chats == nil {
		return fmt.Errorf("inbox: Deps.Chats is required")
	}
	if d.ProjectRows == nil {
		return fmt.Errorf("inbox: Deps.ProjectRows is required")
	}
	if d.Machines == nil {
		return fmt.Errorf("inbox: Deps.Machines is required")
	}
	if d.OrgKeys == nil {
		return fmt.Errorf("inbox: Deps.OrgKeys is required")
	}
	if d.Items == nil || d.Firings == nil || d.Notices == nil || d.Registrations == nil {
		return fmt.Errorf("inbox: Deps.Items, Firings, Notices and Registrations are required")
	}
	if d.Recoveries == nil {
		return fmt.Errorf("inbox: Deps.Recoveries is required")
	}
	if d.Marks == nil {
		return fmt.Errorf("inbox: Deps.Marks is required")
	}
	if d.Storage == nil {
		return fmt.Errorf("inbox: Deps.Storage is required")
	}
	return nil
}

// Service implements the inbox Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedInboxServiceHandler
	deps Deps
	// storageCache holds each user's storage view for storageViewTTL.
	storageCache sync.Map
	// inboxReads shares one user's concurrent ListInbox reads (sharedRead).
	inboxReads freshFlight[*inboxReads]
	// adminOrgs holds each user's administered orgs (adminOrgIDs) for adminOrgsTTL.
	adminOrgs sync.Map
	// teamOrgs holds whether an org is a team (two or more live members) for
	// teamOrgTTL, keyed by org.
	teamOrgs sync.Map
}

// New creates a new inbox service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "inbox"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewInboxServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_inbox_proto
