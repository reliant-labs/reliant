// Package hosteduse gives live fixed Action activities one-use, request-bound
// authority to ask the hosted controller to execute HTTP. It never reads a
// hosted secret. The controller receives only an opaque ticket and redeems it
// through the API; caller-supplied ownership is never accepted.
//
//forge:provided
package hosteduse

import (
	"context"
	"net/http"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/hostedhttp"
)

// Service is the fixed activity's issuer and remote execution boundary.
type Service interface {
	Issue(ctx context.Context, runID, nodeID, grantID string, req hostedhttp.Request) (string, error)
	ExecuteAction(ctx context.Context, runID, nodeID, grantID string, req hostedhttp.Request) (*hostedhttp.Result, error)
}

// Store persists ticket authority derived from active runs and atomically
// redeems an exact match. A nil redemption or false insertion is a denial.
type Store interface {
	CreateHostedHTTPActionTicket(context.Context, core.HostedHTTPActionTicket) (bool, error)
	RedeemHostedHTTPActionTicket(context.Context, string, string, string) (*core.HostedHTTPActionTicket, error)
}

// Deps configures the durable store and separately authenticated controller hop.
type Deps struct {
	Store   Store
	BaseURL string
	Sign    func() (string, error)
	// CAFile is a dedicated PEM trust bundle for the private controller.
	// It is re-read on each new TLS connection to follow projected CA updates.
	//forge:optional-dep
	CAFile string
	// HTTPClient is optional; the client is copied and redirects disabled.
	//forge:optional-dep
	HTTPClient *http.Client
}

// RedeemRequest names the exact authority the controller intends to consume.
type RedeemRequest struct {
	TicketID      string `json:"ticket_id"`
	GrantID       string `json:"grant_id"`
	RequestSHA256 string `json:"request_sha256"`
}

// Metadata is trusted persisted run identity. WorkflowID is a runtime execution
// ID, not a stable definition ID; name+project+owner and node bind the grant.
// This contract does not attest an immutable workflow source snapshot.
type Metadata struct {
	OwnerUserID  string    `json:"owner_user_id"`
	ProjectID    string    `json:"project_id"`
	WorkflowID   string    `json:"workflow_id"`
	WorkflowName string    `json:"workflow_name"`
	NodeID       string    `json:"node_id"`
	RunID        string    `json:"run_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}
