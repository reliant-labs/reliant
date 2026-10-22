package attachment

import (
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the attachment service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo stores attachments and their bytes.
	Repo Repository
	// MaxFileSize is the largest upload accepted, in bytes (the API server's
	// MAX_UPLOAD_SIZE). Zero takes the default, 10 MB.
	MaxFileSize int64
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("attachment: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("attachment: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("attachment: Deps.Repo is required")
	}
	return nil
}

// Service implements the attachment Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedAttachmentServiceHandler
	deps Deps
}

// New creates a new attachment service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
// A zero Deps.MaxFileSize takes its default, 10 MB.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	if deps.MaxFileSize == 0 {
		deps.MaxFileSize = defaultMaxFileSize
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "attachment"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewAttachmentServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_attachment_proto
