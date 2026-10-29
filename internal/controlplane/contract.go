// Package controlplane is reliant's outbound adapter to the managed control
// plane (github.com/reliant-labs/control-plane).
//
// forge:outbound-io
//
// It owns the wire format of every call reliant makes to the control plane —
// the generated Connect clients for the public services, and plain Connect
// HTTP+JSON for DaemonInternalService, which the exported contract withholds —
// along with the bearer each call presents and the control-plane → domain
// mapping. Nothing calls in.
//
// Service is one method per operation reliant needs. Consumers do not take
// Service whole: each declares the one- or two-method slice it uses beside
// itself (account.ControlPlane, project.CloneDispatcher,
// trigger.AutomationTokens, settings.LLMKeyMinter, toolexec.DaemonResumer,
// toolexec.RunResumer), so a new operation here widens no consumer. `forge
// generate` writes a mock of Service into mock_gen.go; adapter tests stand up
// an httptest server.
//
// Built by hand, by internal/app's providers (providers_api.go,
// providers_worker.go) and internal/mcpserver: never a node compose.go
// constructs from Deps.
//
//forge:provided
package controlplane

import (
	"context"
	"net/http"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// Service is every operation reliant performs against the control plane.
type Service interface {
	// MintLLMKey mints the caller's LLM gateway key for deviceName, atomically
	// revoking that device's previous key.
	MintLLMKey(ctx context.Context, jwt, deviceName string) (LLMKey, error)

	// MintDaemonResumeToken mints the caller's delegated automation credential
	// for ONE daemon: a daemon-bound `daemon:resume` token that can resolve
	// and wake that daemon and nothing else. Rotate replaces the previous
	// token for the same name, so re-minting never accumulates live secrets.
	MintDaemonResumeToken(ctx context.Context, jwt, daemonID, name string) (DaemonResumeToken, error)

	// RevokeDaemonResumeTokens revokes every live `daemon:resume` token the
	// caller holds that is bound to daemonID.
	RevokeDaemonResumeTokens(ctx context.Context, jwt, daemonID string) error

	// PreviewAccountDeletionWallet asks the control plane what deleting the
	// caller's account would refund and forfeit. Changes nothing.
	PreviewAccountDeletionWallet(ctx context.Context, jwt string) (*AccountDeletionWalletQuote, error)

	// DeleteCurrentUserAccount asks the control plane to settle the caller's
	// wallet (refund paid credit, forfeit promotional credit) and tombstone
	// their platform account (billing identity, daemons, PII), forwarding the
	// caller's own JWT.
	//
	// Non-empty Blockers means the control plane REFUSED and destroyed
	// nothing — the caller must surface the blockers and stop. An error means
	// the call itself failed; a connect.CodeUnavailable error means a refund's
	// outcome could not be confirmed and nothing else was deleted, so the
	// caller may retry.
	DeleteCurrentUserAccount(ctx context.Context, jwt string) (*AccountDeletionResult, error)

	// CloneRepoOntoDaemon asks the control plane to clone a repo onto one of
	// the caller's daemons, using the git credential IT holds — reliant has
	// no access to the user's GitHub token, which is why this is a call and
	// not something reliant does itself.
	//
	// It returns once the clone is QUEUED, not once it has run: the control
	// plane enqueues durably so a daemon that is asleep or booting is still
	// a valid target. Queued=true means the checkout does not exist yet.
	CloneRepoOntoDaemon(ctx context.Context, jwt string, req CloneRepoRequest) (CloneRepoResult, error)

	// ResumeDaemon asks the control plane to wake one suspended managed
	// daemon. It returns once the wake is under way, not once the daemon is
	// attached.
	//
	// token is sent as the Bearer and is what the control plane derives the
	// owner from: a user's JWT, or a `daemon:resume` access token bound to
	// exactly this daemon (control-plane's svcdaemon.ownerForDaemon). There is
	// no service credential, so a resume can only ever wake a daemon its
	// caller owns.
	//
	// Errors are the control plane's Connect errors, unwrapped, so callers
	// can read the code: FailedPrecondition means the control plane does not
	// hold this daemon as suspended (already resumed, or not a managed
	// machine).
	ResumeDaemon(ctx context.Context, token, daemonID string) error

	// SuspendDaemon stops one of the caller's managed daemons, as Settings ->
	// Machines does. jwt is the owner's session token: the control plane
	// refuses a machine credential ("missing owner in context").
	SuspendDaemon(ctx context.Context, jwt, daemonID string) error

	// ResizeDaemon changes a SUSPENDED managed daemon's size (small, medium,
	// large, xl, 2xl); it takes effect when the daemon next starts. A running
	// daemon is FailedPrecondition. jwt is the owner's session token, as for
	// SuspendDaemon.
	ResizeDaemon(ctx context.Context, jwt, daemonID, size string) error

	// ResumeDaemonForRun wakes a run owner's suspended machine for a run that
	// resumes after a hold, with this process's internal-service identity
	// (Deps.Sign) rather than the owner's session token, which may have
	// expired while the run was held. The control plane still runs the
	// owner's own resume gates (ownership, workspace limit, funding).
	//
	// resumed reports whether this call started the wake. A control plane
	// without the door (404, or Unimplemented), or an adapter with no
	// identity to sign with, answers toolexec.ErrRunResumeUnsupported. Other
	// refusals are a *RunResumeError carrying the control plane's
	// client-safe message.
	ResumeDaemonForRun(ctx context.Context, userID, daemonID string) (resumed bool, err error)

	// UpsertAutopilotReceiver creates or replaces the control plane's alert
	// receiver for one Autopilot automation (a webhook trigger declared
	// `source: alerting`), in its owner's primary organization: every alert of
	// the scoped environments that passes the receiver's default filter is
	// delivered to r.URL with r.Token as the bearer. Signed with this
	// process's internal-service identity (Deps.Sign).
	//
	// Errors: ErrAutopilotUnsupported when the control plane has no such
	// service or this adapter cannot sign; otherwise an *AutopilotError, whose
	// Reason is ReasonAutopilotNoOrg for an owner with no organization.
	UpsertAutopilotReceiver(ctx context.Context, r AutopilotReceiver) error

	// DeleteAutopilotReceiver removes one automation's receiver. Removing
	// one that does not exist succeeds.
	DeleteAutopilotReceiver(ctx context.Context, activationID string) error

	// ListAutopilotReceivers returns the activation id of every autopilot
	// receiver the control plane holds, across every organization, for the
	// reconcile sweep.
	ListAutopilotReceivers(ctx context.Context) ([]string, error)
}

// Deps configures the adapter.
type Deps struct {
	// ControlPlane is where the control plane is: its url
	// (RELIANT_CONTROL_PLANE_URL) is the origin every call goes to. Nil, or
	// an empty url, dials the local default, for development against a
	// control plane on this machine; a deployment that has no control plane
	// does not build the adapter at all (Endpoint.Hosted).
	ControlPlane *configv1.ControlPlaneConfig
	// Sign mints the internal-service bearer ResumeDaemonForRun presents.
	// Nil — no INTERNAL_SERVICE_SECRET — leaves that one call unsupported;
	// every other call presents the caller's own token.
	Sign func() (string, error)
	// HTTPClient carries every call. Nil uses a client with a 30s timeout.
	HTTPClient *http.Client
}
