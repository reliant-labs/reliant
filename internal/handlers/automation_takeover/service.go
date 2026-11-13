package automation_takeover

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The org's takeover of a departed member's automations
// (control-plane docs/design/org-sharing.md §8). This service decides WHO
// may take a trigger and WHEN: an org admin (role admin or owner, read
// fresh, a person) adopting to themselves, or the member an admin offered it
// to accepting a live offer. WHAT adoption does to the trigger, re-validating
// every reference as the new owner's, is the trigger service's
// (trigger.Service.AdoptAs), reached through Triggers.
//
// The rule that shapes it: every run a trigger launches executes as its
// owner, so nobody becomes the owner of one without being the one who
// accepts it. An admin never pushes an automation onto a member.

// OfferTTL is how long an offer waits for its offeree.
const OfferTTL = 7 * 24 * time.Hour

// Deps holds dependencies for the automation_takeover service. Composition
// (internal/app/compose.go) supplies them.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads an org's triggers and saved workflows and keeps the offers.
	Repo Repository

	// Triggers adopts a trigger as its new owner and renders one for
	// someone who does not own it. *trigger.Service is it.
	Triggers TriggerAdopter

	// Now is the clock offers expire by; nil is time.Now.
	// forge:optional-dep
	Now func() time.Time
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("automation_takeover: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("automation_takeover: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("automation_takeover: Deps.Repo is required")
	}
	if d.Triggers == nil {
		return fmt.Errorf("automation_takeover: Deps.Triggers is required")
	}
	return nil
}

// Service implements the automation_takeover Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedAutomationTakeoverServiceHandler
	deps Deps
	// now is Deps.Now, or time.Now when it is nil.
	now func() time.Time
}

// New creates a new automation_takeover service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Service{deps: deps, now: now}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "automation_takeover"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewAutomationTakeoverServiceHandler(s, opts...)
	mux.Handle(path, handler)
}

// RegisterHTTP registers plain HTTP handlers outside Connect RPC. This
// service has none.
func (s *Service) RegisterHTTP(_ *http.ServeMux, _ func(http.Handler) http.Handler) {}
