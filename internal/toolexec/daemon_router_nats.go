// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonliveness"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/observability"
)

// NATS subject patterns for tool execution and daemon routing.
// Subjects now include {userID}.{daemonID} for multi-daemon routing.
const (
	toolRequestSubject = "tools.request"    // tools.request.{userID}.{daemonID}
	toolCancelSubject  = "tools.cancel"     // tools.cancel.{userID}.{daemonID}
	toolBackgroundSubj = "tools.background" // tools.background.{userID}.{daemonID}
	toolOnlineSubject  = "tools.online"     // tools.online.{userID}.{daemonID}

	daemonKillSubject      = "daemon.process.kill" // daemon.process.kill.{userID}.{daemonID}
	daemonCommandSubject   = "daemon.command"      // daemon.command.{userID}.{daemonID}
	toolRequestSyncSubject = "tools.request.sync"  // tools.request.sync.{userID}.{daemonID}

	configLoadSubject  = "daemon.config.load"  // daemon.config.load.{userID}.{daemonID}
	configWatchSubject = "daemon.config.watch" // daemon.config.watch.{userID}.{daemonID}

	// Terminal streaming subjects
	terminalInputSubject     = "daemon.terminal.input"     // daemon.terminal.input.{userID}.{daemonID}.{sessionID}
	terminalResizeSubject    = "daemon.terminal.resize"    // daemon.terminal.resize.{userID}.{daemonID}.{sessionID}
	terminalOutputSubject    = "daemon.terminal.output"    // daemon.terminal.output.{userID}.{daemonID}.{sessionID}
	terminalSubscribeSubject = "daemon.terminal.subscribe" // daemon.terminal.subscribe.{userID}.{daemonID}.{sessionID}

	// Process output streaming subjects
	processOutputSubscribeSubject = "daemon.process.subscribe" // daemon.process.subscribe.{userID}.{daemonID}
	processOutputSubject          = "daemon.process.output"    // daemon.process.output.{userID}.{processID}
)

// daemonSubject builds a NATS subject with the pattern base.{userID}.{daemonID}.
func daemonSubject(base, userID, daemonID string) string {
	return base + "." + userID + "." + daemonID
}

// NATSDaemonRouter implements DaemonRouter using NATS pub/sub.
// Used by workers and api-server replicas in distributed mode to route
// daemon operations to the api-server that holds the daemon's gRPC connection.
type NATSDaemonRouter struct {
	nc          *nats.Conn
	db          db.Repository           // the daemon registry's records: resolution reads them directly
	resolver    DaemonResolver          // optional: used to resolve daemonID for a user
	resumer     DaemonResumer           // optional: wakes a suspended managed daemon (control plane)
	credentials ControlPlaneCredentials // optional: token source for resumes

	// inflight remembers which daemon each tool request went to, so a cancel
	// or background for it reaches THAT daemon rather than the user's default.
	inflight inflightDaemons

	// jsOnce lazily initializes the JetStream context the first time
	// EnqueueDaemonCommand is called. JetStream is only used for the
	// pending-commands stream — the rest of the router is core NATS only,
	// and we don't want to pay the JetStream startup cost on every router
	// that never enqueues.
	jsOnce sync.Once
	js     jetstream.JetStream
	jsErr  error
}

// NewNATSDaemonRouter creates a new NATS-based daemon router.
func NewNATSDaemonRouter(nc *nats.Conn, opts ...NATSRouterOption) *NATSDaemonRouter {
	r := &NATSDaemonRouter{nc: nc}
	for _, o := range opts {
		o(r)
	}
	return r
}

// NATSRouterOption configures a NATSDaemonRouter.
type NATSRouterOption func(*NATSDaemonRouter)

// WithDatabase adds a DB repository for fast daemon-online checks.
func WithDatabase(repo db.Repository) NATSRouterOption {
	return func(r *NATSDaemonRouter) {
		r.db = repo
	}
}

// WithResolver sets the DaemonResolver for multi-daemon routing.
func WithResolver(resolver DaemonResolver) NATSRouterOption {
	return func(r *NATSDaemonRouter) {
		r.resolver = resolver
	}
}

// DaemonResumer wakes one suspended managed daemon. Only the control plane can:
// the machine's Workspace CR is its, and so is the decision to spend compute
// on it. Declared here, at the consumer; *controlplane.DaemonClient
// (controlplane.v1.DaemonService/ResumeDaemon) implements it.
//
// token is the raw credential the control plane derives the owner from: the
// user's JWT, or a daemon:resume token bound to daemonID. An error carrying
// connect.CodeFailedPrecondition means the control plane does not hold the
// daemon as suspended.
type DaemonResumer interface {
	ResumeDaemon(ctx context.Context, token, daemonID string) error
}

// WithDaemonResumer lets EnsureAwake wake a suspended managed daemon. Without
// it (no control plane configured) a suspended daemon stays ErrDaemonPending.
//
// There is deliberately no control-plane LOOKUP option: daemon records are the
// registry's (docs/design/one-daemon-list.md), and the router reads them from
// its own database.
func WithDaemonResumer(resumer DaemonResumer) NATSRouterOption {
	return func(r *NATSDaemonRouter) {
		r.resumer = resumer
	}
}

// ControlPlaneCredentials supplies the token for a control-plane resume made
// on a user's behalf. Declared here, at the consumer.
//
// BearerFor returns the user's JWT when there is one; otherwise, ONLY when
// daemonID is non-empty, the delegated token bound to exactly that daemon;
// otherwise "".
type ControlPlaneCredentials interface {
	BearerFor(ctx context.Context, userID, daemonID string) (string, error)
}

// ErrAutomationAccessNotGranted: no user JWT and no delegated token for the
// pinned daemon, so the control plane cannot be asked to wake it.
var ErrAutomationAccessNotGranted = errors.New(
	"automation access not granted for this machine: sign in to reliant to re-enable the trigger")

// errNoResumeCredential: no user JWT to wake the machine with. The api-server's
// attended path checks for one before asking, so this is a backstop.
var errNoResumeCredential = errors.New("waking this machine needs a signed-in session")

// WithControlPlaneCredentials sets where the router gets resume tokens. Only
// the worker wires this; without it the router uses the user's JWT alone.
func WithControlPlaneCredentials(c ControlPlaneCredentials) NATSRouterOption {
	return func(r *NATSDaemonRouter) { r.credentials = c }
}

// resolveDefaultDaemonID resolves the default daemon ID for a user (no
// selector); see resolveDaemonID.
func (r *NATSDaemonRouter) resolveDefaultDaemonID(ctx context.Context, userID string) (string, error) {
	return r.resolveDaemonID(ctx, userID, nil)
}

// resolveFollowUpDaemonID picks the daemon for an operation that follows an
// earlier request (cancel, background, kill, terminal I/O, config load).
// Order: the daemon that request was sent to (requestID, when known), then the
// run's selector carried in ctx, and only then the user's default — the case
// where nothing about the run named a daemon at all.
func (r *NATSDaemonRouter) resolveFollowUpDaemonID(ctx context.Context, userID string, requestIDs ...string) (string, error) {
	for _, id := range requestIDs {
		if daemonID, ok := r.inflight.lookup(userID, id); ok {
			return daemonID, nil
		}
	}
	return r.resolveDaemonID(ctx, userID, DaemonSelectorFromContext(ctx))
}

// ResolveDaemonID exposes the same default resolution SendDaemonCommand uses,
// so callers can record which daemon an operation targeted.
func (r *NATSDaemonRouter) ResolveDaemonID(ctx context.Context, userID string) (string, error) {
	return r.resolveDefaultDaemonID(ctx, userID)
}

// resolveDaemonID resolves a daemon ID for a user, optionally using a selector.
// Resolution order:
//  1. Local resolver (connected daemons on this gateway)
//  2. The daemon registry's own records (lookupDaemonRecord) — read from this
//     process's database, never fetched from anyone
//
// Resolution never wakes anything: a suspended daemon yields ErrDaemonPending.
// Waking is EnsureAwake's job alone.
func (r *NATSDaemonRouter) resolveDaemonID(ctx context.Context, userID string, selector *DaemonSelector) (string, error) {
	// A run with no machine never resolves one. Every send path resolves
	// through here, so this one check closes them all.
	if nomachine.Is(ctx) {
		return "", nomachine.ErrNoMachine
	}

	// Step 1: Try local resolver (connected daemons).
	if r.resolver != nil {
		daemons, err := r.resolver.ResolveDaemons(ctx, userID, selector)
		if err != nil {
			return "", err
		}
		if len(daemons) > 0 {
			// When no selector, prefer local daemons.
			if selector == nil {
				for _, d := range daemons {
					if d.Type == "local" {
						return d.DaemonID, nil
					}
				}
			}
			return daemons[0].DaemonID, nil
		}
		// No connected daemons matched — fall through to the registry's records.
	}

	// Step 2: the registry's records.
	rec, found, sawDaemonRecord, err := r.lookupDaemonRecord(ctx, userID, selector)
	if err != nil {
		return "", err
	}
	return routableDaemonID(userID, selector, rec, found, sawDaemonRecord)
}

// daemonRecordState is how routing reads one of the user's daemon records,
// from the two things the registry knows about it: the attachment lease it
// owns, and the lifecycle phase the control plane mirrors in over
// daemon.v1.state.<id>.lifecycle. It is the same join
// DaemonRegistryService's composeDaemonStatus performs for the UI, with
// attachment winning for the same reason: a stream attached right now is
// observed, the phase is a slightly older mirror.
type daemonRecordState int

const (
	// recordAttached: a fresh attachment lease — routable now.
	recordAttached daemonRecordState = iota
	// recordUnconfirmed: no fresh lease, and no lifecycle phase saying the
	// machine is down — every self-hosted daemon (which never reports one),
	// or a managed one reporting ready or failed. The lease is a decaying
	// hint, so the NATS request decides reachability.
	recordUnconfirmed
	// recordStarting: provisioning or cloning. Coming up; not routable yet.
	recordStarting
	// recordSuspended: suspending or suspended. Parked until EnsureAwake
	// resumes it.
	recordSuspended
)

func daemonRecordStateOf(d *db.Daemon, attached bool) daemonRecordState {
	if attached {
		return recordAttached
	}
	if d.LifecyclePhase == nil {
		return recordUnconfirmed
	}
	switch daemonstate.LifecyclePhase(*d.LifecyclePhase) {
	case daemonstate.LifecyclePhaseProvisioning, daemonstate.LifecyclePhaseCloning:
		return recordStarting
	case daemonstate.LifecyclePhaseSuspending, daemonstate.LifecyclePhaseSuspended:
		return recordSuspended
	default:
		return recordUnconfirmed
	}
}

// daemonRecord is the record routing chose for a request.
type daemonRecord struct {
	id      string
	state   daemonRecordState
	managed bool // a cloud machine the control plane can suspend and resume
}

// wakeable reports whether EnsureAwake should ask the control plane to resume
// this daemon.
//
// A suspended record, always. But the registry's lifecycle is a MIRROR of
// controlplane.daemons: it can lag a transition, and a machine suspended
// before the mirror existed has no phase at all. So an unattached managed
// machine is asked about too, unless the mirror says it is already on its way
// up. The control plane is the authority, and refuses a daemon it does not
// hold as suspended — which EnsureAwake reads as "already awake". This is
// what keeps the attended wake from depending on the mirror being current;
// resolution, the hot path, still reads the mirror alone.
func (rec daemonRecord) wakeable() bool {
	switch rec.state {
	case recordSuspended:
		return true
	case recordUnconfirmed:
		return rec.managed
	default:
		return false
	}
}

// lookupDaemonRecord picks the daemon a request targets from the registry's
// own records — the daemons table and daemon_attachment in this process's
// database. It makes no network call: these records are the registry's
// (docs/design/one-daemon-list.md), so there is nobody else to ask.
//
// found is false when no record matches the selector. sawDaemonRecord reports
// whether the user has ANY daemon record, which is what separates "this user
// will never have a daemon" from "their daemon is still coming up" — the
// latter must be ErrDaemonPending (retryable), not a flat error, or the UI
// cannot tell a provisioning machine from a genuinely absent one.
//
// Preference among matches, first match of each:
//  1. attached — routable now;
//  2. starting or suspended — a managed machine whose lifecycle says it is on
//     its way up, or that EnsureAwake can wake. Ranked above (3) because an
//     unattached record with no lifecycle is most often a self-hosted daemon
//     that has gone away, and handing NATS its id for a user whose cloud
//     machine is merely parked would skip the wake entirely;
//  3. unconfirmed — the lease is a decaying hint, not ground truth (NATS
//     answers ErrNoResponders → CodeUnavailable for a daemon with no live
//     subscription), so a connected-but-idle daemon whose lease went stale
//     stays routable rather than erroring here.
func (r *NATSDaemonRouter) lookupDaemonRecord(ctx context.Context, userID string, selector *DaemonSelector) (rec daemonRecord, found, sawDaemonRecord bool, err error) {
	if r.db == nil {
		return daemonRecord{}, false, false, nil
	}
	daemons, err := r.db.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return daemonRecord{}, false, false, fmt.Errorf("resolving daemon ID from DB: %w", err)
	}
	attachedIDs, err := r.db.ListAttachedDaemonIDsForUser(ctx, userID, daemonStaleThreshold)
	if err != nil {
		return daemonRecord{}, false, false, fmt.Errorf("resolving attached daemon IDs from DB: %w", err)
	}
	attached := make(map[string]bool, len(attachedIDs))
	for _, id := range attachedIDs {
		attached[id] = true
	}

	var lifecycleDown, unconfirmed *daemonRecord
	for _, d := range daemons {
		if !daemonRecordMatches(d, selector) {
			continue
		}
		// Only a record the selector matches means "a machine for this
		// request exists". Counting the user's other daemons would make an id
		// they do not own (or that does not exist) read as "still starting".
		sawDaemonRecord = true
		candidate := daemonRecord{
			id:      d.ID,
			state:   daemonRecordStateOf(d, attached[d.ID]),
			managed: d.DaemonType != nil && canonicalDaemonType(*d.DaemonType) == "managed",
		}
		switch candidate.state {
		case recordAttached:
			return candidate, true, true, nil
		case recordStarting, recordSuspended:
			if lifecycleDown == nil {
				lifecycleDown = &candidate
			}
		default:
			if unconfirmed == nil {
				unconfirmed = &candidate
			}
		}
	}
	if lifecycleDown != nil {
		return *lifecycleDown, true, sawDaemonRecord, nil
	}
	if unconfirmed != nil {
		return *unconfirmed, true, sawDaemonRecord, nil
	}
	return daemonRecord{}, false, sawDaemonRecord, nil
}

// daemonRecordMatches applies a selector to a registry record. Records carry
// no labels, so label criteria are not applied here (the connected resolver,
// which has them, already ran).
func daemonRecordMatches(d *db.Daemon, selector *DaemonSelector) bool {
	if selector == nil {
		return true
	}
	if selector.ID != "" && selector.ID != d.ID {
		return false
	}
	if selector.Name != "" && (d.Hostname == nil || *d.Hostname != selector.Name) {
		return false
	}
	if want := canonicalDaemonType(selector.Type); want != "" {
		if d.DaemonType == nil || canonicalDaemonType(*d.DaemonType) != want {
			return false
		}
	}
	return true
}

// canonicalDaemonType folds the two spellings of daemon type onto the
// record's: selectors say "cloud"/"local", records say "managed"/"self_hosted".
// "" and "any" select every daemon.
func canonicalDaemonType(t string) string {
	switch t = strings.ToLower(strings.TrimSpace(t)); t {
	case "", "any":
		return ""
	case "cloud", "managed":
		return "managed"
	case "local", "self_hosted", "self-hosted":
		return "self_hosted"
	default:
		return t
	}
}

// routableDaemonID turns a looked-up record into the id to send to, or the
// error explaining why there is none yet. It never wakes anything.
func routableDaemonID(userID string, selector *DaemonSelector, rec daemonRecord, found, sawDaemonRecord bool) (string, error) {
	if found {
		switch rec.state {
		case recordAttached, recordUnconfirmed:
			return rec.id, nil
		case recordSuspended:
			logging.Info("[DaemonRouter] daemon is suspended; not waking it from tool-time resolution",
				append([]any{"user_id", userID, "daemon_id", rec.id}, selectorLogFields(selector)...)...)
			return "", fmt.Errorf("the machine for this request is suspended and will wake when you next message it: %w", ErrDaemonPending)
		}
		// recordStarting falls through to the "still starting" error below.
	}

	// ── The user id stays in the LOG, never in the message ───────────
	//
	// Both errors below are rendered VERBATIM to the end user:
	// mapDaemonDispatchError wraps them into a Connect error whose message the
	// UI prints. The account UUID that used to be interpolated here therefore
	// surfaced as "[internal] resolving daemon for command: no daemon
	// available for user 22302879-fd98-4cde-9e12-532b12a5d3fc" — an internal
	// identifier shown to the person it identifies, describing our plumbing
	// rather than telling them anything they can act on.
	//
	// The id is exactly what an OPERATOR needs, so it is logged with the
	// selector beside it. What crosses to the user is a sentence about their
	// machine.

	// A daemon record exists (provisioning, or created but not yet attached)
	// but nothing above could route to it. The "please wait" case, not a hard
	// failure — ErrDaemonPending lets callers tell it from "no daemon at all",
	// and its own text carries the "no daemon connected" marker the frontend's
	// wait machinery keys on (isDaemonConnectingError / classifyDaemonWait).
	if sawDaemonRecord {
		logging.Warn("[DaemonRouter] daemon record exists but is not routable yet",
			append([]any{"user_id", userID}, selectorLogFields(selector)...)...)
		if selector != nil {
			return "", fmt.Errorf("the machine for this request is still starting: %w", ErrDaemonPending)
		}
		return "", fmt.Errorf("your machine is still starting: %w", ErrDaemonPending)
	}

	logging.Warn("[DaemonRouter] no daemon could be resolved",
		append([]any{"user_id", userID}, selectorLogFields(selector)...)...)
	if selector != nil {
		return "", fmt.Errorf("no daemon available: the machine this request asked for is not connected")
	}
	return "", fmt.Errorf("no daemon available: no machine is connected to your account yet")
}

// selectorLogFields renders an optional selector as structured log key/values,
// so a nil selector logs empty strings rather than being dropped or panicking.
func selectorLogFields(selector *DaemonSelector) []any {
	if selector == nil {
		return []any{"selector_type", "", "selector_name", "", "selector_id", ""}
	}
	return []any{
		"selector_type", selector.Type,
		"selector_name", selector.Name,
		"selector_id", selector.ID,
	}
}

// DaemonWaker wakes a suspended daemon. It is held only by the callers that
// are allowed to: run preflight, the attended send/start path, and a signed-in
// user's own file and worktree requests (services.machineWake). Tool-time code
// holds DaemonRouter, which has no such method.
type DaemonWaker interface {
	EnsureAwake(ctx context.Context, userID string, selector *DaemonSelector) (daemonID string, err error)
}

var _ DaemonWaker = (*NATSDaemonRouter)(nil)

// WakeResult is what a wake did.
type WakeResult struct {
	// DaemonID is the daemon the selector resolved to.
	DaemonID string
	// Resumed is true when the control plane accepted a request to resume the
	// daemon: it was asleep, and is now on its way up. False when it was
	// already up, or already on its way (provisioning, or woken by an earlier
	// call).
	Resumed bool
}

// EnsureAwake returns the id of the daemon for the selector, resuming it
// through the control plane when it may be suspended. See Wake.
func (r *NATSDaemonRouter) EnsureAwake(ctx context.Context, userID string, selector *DaemonSelector) (string, error) {
	res, err := r.Wake(ctx, userID, selector)
	return res.DaemonID, err
}

// Wake is EnsureAwake, reporting whether it resumed the daemon
// (daemonRecord.wakeable decides whether to ask). It is the ONLY code in this
// router that resumes anything.
//
// The daemon is chosen from the registry's own records, exactly as resolution
// chooses it; only the wake itself leaves this process, and an attached daemon
// costs no call at all. Success means the wake is under way, not that the
// daemon is attached yet — tool calls in the meantime get ErrDaemonPending
// until it is.
//
// The token is the user's JWT, or, for a ctx marked by automationcred.Allow,
// the delegated token bound to the selector's PINNED daemon.
func (r *NATSDaemonRouter) Wake(ctx context.Context, userID string, selector *DaemonSelector) (WakeResult, error) {
	if nomachine.Is(ctx) {
		return WakeResult{}, nomachine.ErrNoMachine
	}
	if r.resolver != nil {
		if daemons, err := r.resolver.ResolveDaemons(ctx, userID, selector); err == nil && len(daemons) > 0 {
			return WakeResult{DaemonID: daemons[0].DaemonID}, nil
		}
	}
	rec, found, sawDaemonRecord, err := r.lookupDaemonRecord(ctx, userID, selector)
	if err != nil {
		return WakeResult{}, err
	}
	if !found || r.resumer == nil || !rec.wakeable() {
		daemonID, err := routableDaemonID(userID, selector, rec, found, sawDaemonRecord)
		return WakeResult{DaemonID: daemonID}, err
	}

	resumed, err := r.resume(ctx, userID, selector, rec.id)
	if err != nil && rec.state != recordSuspended {
		// Not known to be suspended: the resume only hedged against a lagging
		// mirror, and its failure says nothing the NATS request will not. Route
		// exactly as resolution would have.
		logging.Info("[DaemonRouter] speculative resume of an unattached managed daemon failed; routing to it as-is",
			"user_id", userID, "daemon_id", rec.id, "error", err)
		daemonID, err := routableDaemonID(userID, selector, rec, found, sawDaemonRecord)
		return WakeResult{DaemonID: daemonID}, err
	}
	if err != nil {
		return WakeResult{}, err
	}
	return WakeResult{DaemonID: rec.id, Resumed: resumed}, nil
}

// resume asks the control plane to wake daemonID, reporting whether it did. A
// FailedPrecondition refusal means the control plane does not hold the daemon
// as suspended — it is already up or on its way (the registry's mirror lagged,
// e.g. a second message sent seconds after the first) — which is what the
// caller wanted, so it is success, with nothing resumed.
func (r *NATSDaemonRouter) resume(ctx context.Context, userID string, selector *DaemonSelector, daemonID string) (bool, error) {
	pinned := ""
	if selector != nil {
		pinned = selector.ID
	}
	token, err := r.resumeToken(ctx, userID, pinned)
	if err != nil {
		return false, err
	}
	if err := r.resumer.ResumeDaemon(ctx, token, daemonID); err != nil {
		if connect.CodeOf(err) == connect.CodeFailedPrecondition {
			logging.Info("[DaemonRouter] control plane does not hold the daemon as suspended; treating it as awake",
				"user_id", userID, "daemon_id", daemonID, "error", err)
			return false, nil
		}
		return false, fmt.Errorf("control plane ResumeDaemon(%s): %w", daemonID, err)
	}
	return true, nil
}

// resumeToken picks the credential a resume is sent with. The credentials
// source (worker only) may fall back to a trigger's delegated token, and it is
// asked for the PINNED daemon — never whichever daemon default resolution
// chose — so an unattended run can wake only the daemon its trigger names.
func (r *NATSDaemonRouter) resumeToken(ctx context.Context, userID, pinnedDaemonID string) (string, error) {
	if r.credentials != nil {
		token, err := r.credentials.BearerFor(ctx, userID, pinnedDaemonID)
		if err != nil {
			return "", fmt.Errorf("loading control-plane credential: %w", err)
		}
		if token == "" {
			return "", ErrAutomationAccessNotGranted
		}
		return token, nil
	}
	if jwt, ok := auth.GetUserJWT(userID); ok && jwt != "" {
		return jwt, nil
	}
	return "", errNoResumeCredential
}

// daemonStaleThreshold is 6 missed 15s heartbeats, matching the gateway's
// staleConnectionThreshold and daemonliveness.DefaultStaleThreshold. If the
// frontend reports a heartbeat within this window we skip the DB query. The
// old 30s value (2 missed heartbeats) left zero margin over the heartbeat
// interval, so routing intermittently judged a live daemon offline.
const daemonStaleThreshold = 90 * time.Second

// LocalConnectionChecker is implemented by resolvers that can report whether
// any daemon stream is currently held in-process for a given user. It lets the
// router skip DB-based liveness checks for single-replica deployments.
type LocalConnectionChecker interface {
	HasConnectedDaemonsForUser(userID string) bool
}

// IsDaemonOnline is a thin wrapper that delegates to
// daemonliveness.ReachableByUser. The signature is preserved so call sites
// don't change during the Step 1 migration.
//
// When no DB is configured (OSS single-replica) the router falls back to the
// legacy NATS request-reply path, which enumerates and asks; otherwise the
// daemonliveness package owns the answer.
func (r *NATSDaemonRouter) IsDaemonOnline(ctx context.Context, userID string) (bool, error) {
	if r.db == nil {
		return r.isDaemonOnlineViaNATS(ctx, userID)
	}
	s, err := daemonliveness.ReachableByUser(ctx, r.nc, dbAdapter{r.db}, userID)
	if err != nil {
		return false, fmt.Errorf("checking daemon liveness: %w", err)
	}
	return s.Live, nil
}

// dbAdapter implements daemonliveness.Repository by delegating to the
// existing db.Repository. We deliberately do NOT add a new repo method:
// IsDaemonAttached already answers the boolean we need, and LastSeen=zero is
// acceptable for the current callers (none read it). The per-daemon variant
// is a TODO — see below.
type dbAdapter struct {
	repo db.Repository
}

func (a dbAdapter) GetUserLiveness(ctx context.Context, userID string, staleThreshold time.Duration) (daemonliveness.Status, error) {
	live, err := a.repo.IsDaemonAttached(ctx, userID, staleThreshold)
	if err != nil {
		return daemonliveness.Status{}, err
	}
	return daemonliveness.Status{Live: live}, nil
}

func (a dbAdapter) GetDaemonLiveness(ctx context.Context, daemonID string, staleThreshold time.Duration) (daemonliveness.Status, error) {
	// TODO: add a per-daemon attachment-freshness query to db.Repository when
	// the first caller for Reachable(daemonID) lands. For now, callers route
	// through ReachableByUser via IsDaemonOnline, so this path is unused.
	_ = ctx
	_ = daemonID
	_ = staleThreshold
	return daemonliveness.Status{}, fmt.Errorf("dbAdapter.GetDaemonLiveness: not implemented; no caller yet")
}

// isDaemonOnlineViaNATS is the legacy NATS request-reply check, used as fallback
// when no DB is configured.
func (r *NATSDaemonRouter) isDaemonOnlineViaNATS(ctx context.Context, userID string) (bool, error) {
	// Try resolving a specific daemon first; fall back to wildcard.
	daemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return false, nil // No daemon found → offline.
	}
	subject := daemonSubject(toolOnlineSubject, userID, daemonID)
	reqMsg := observability.NATSPublishMsg(ctx, subject, nil)
	start := time.Now()
	msg, err := r.nc.RequestMsg(reqMsg, 2*time.Second)
	observability.NATSRequestDuration.WithLabelValues("tools.online").Observe(time.Since(start).Seconds())
	if err != nil {
		// No subscribers means no api-server holds this daemon's connection.
		// Timeout means the request went out but nobody answered (daemon genuinely offline).
		// Both are definitive "offline" — not infrastructure failures.
		if err == nats.ErrNoResponders || err == nats.ErrTimeout {
			return false, nil
		}
		// Other errors (connection closed, etc.) are infrastructure failures.
		observability.NATSErrorsTotal.WithLabelValues("tools.online", "request").Inc()
		return false, fmt.Errorf("NATS IsDaemonOnline request failed: %w", err)
	}
	return string(msg.Data) == "true", nil
}

func (r *NATSDaemonRouter) SendToolRequest(ctx context.Context, userID string, request *ToolExecutionRequest) error {
	daemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for tool request: %w", err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	subject := daemonSubject(toolRequestSubject, userID, daemonID)
	r.inflight.record(userID, daemonID, request.RequestID, request.ToolCallID)
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("tools.request", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("tools.request").Inc()
	return nil
}

func (r *NATSDaemonRouter) SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error {
	daemonID, err := r.resolveFollowUpDaemonID(ctx, userID, requestID)
	if err != nil {
		return fmt.Errorf("resolving daemon for cancel: %w", err)
	}
	return r.sendCancelToDaemon(ctx, userID, daemonID, requestID, reason)
}

// sendCancelToDaemon publishes a cancel on one named daemon's subject.
func (r *NATSDaemonRouter) sendCancelToDaemon(ctx context.Context, userID, daemonID, requestID, reason string) error {
	payload, err := json.Marshal(map[string]string{
		"request_id": requestID,
		"reason":     reason,
	})
	if err != nil {
		return err
	}
	subject := daemonSubject(toolCancelSubject, userID, daemonID)
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("tools.cancel", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("tools.cancel").Inc()
	return nil
}

// SendToolExecutionBackground publishes a background request for an in-flight
// execution. Fire-and-forget like cancel: the daemon acts on it if the
// execution is still running, and a request that arrives after the command
// finished is simply a no-op there.
func (r *NATSDaemonRouter) SendToolExecutionBackground(ctx context.Context, userID, requestID, toolCallID string) error {
	daemonID, err := r.resolveFollowUpDaemonID(ctx, userID, requestID, toolCallID)
	if err != nil {
		return fmt.Errorf("resolving daemon for background: %w", err)
	}
	payload, err := json.Marshal(map[string]string{
		"request_id":   requestID,
		"tool_call_id": toolCallID,
	})
	if err != nil {
		return err
	}
	subject := daemonSubject(toolBackgroundSubj, userID, daemonID)
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("tools.background", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("tools.background").Inc()
	return nil
}

// daemonRequestError maps a NATS request/reply error to a caller-facing error.
// nats.ErrNoResponders is authoritative: the NATS server reports that no
// subscription is currently live for the daemon's subject (the gateway
// subscribes on connect and unsubscribes on disconnect), i.e. the daemon is not
// connected. That is the ground-truth reachability signal — we deliberately do
// NOT pre-check a decaying DB freshness timestamp, which produced false
// "offline" for connected-but-idle daemons. Any other error (timeouts, wedged
// daemon, transport failure) is surfaced as-is rather than mislabeled as
// "no daemon connected".
func daemonRequestError(op string, err error) error {
	if err == nats.ErrNoResponders {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("no daemon connected for user"))
	}
	return fmt.Errorf("%s via NATS failed: %w", op, err)
}

func (r *NATSDaemonRouter) SendKillProcess(ctx context.Context, userID, processID string) error {
	payload, err := json.Marshal(map[string]string{
		"process_id": processID,
	})
	if err != nil {
		return err
	}

	daemonID, err := r.resolveFollowUpDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for kill: %w", err)
	}
	subject := daemonSubject(daemonKillSubject, userID, daemonID)
	reqMsg := observability.NATSPublishMsg(ctx, subject, payload)
	start := time.Now()
	msg, err := r.nc.RequestMsg(reqMsg, 10*time.Second)
	observability.NATSRequestDuration.WithLabelValues("daemon.process.kill").Observe(time.Since(start).Seconds())
	if err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.process.kill", "request").Inc()
		return daemonRequestError("kill process", err)
	}

	// Check response for error.
	var resp struct {
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err == nil && resp.Error != "" {
		return fmt.Errorf("remote kill failed: %s", resp.Error)
	}
	return nil
}

func (r *NATSDaemonRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	// Marshal + payload preflight BEFORE daemon resolution: an oversize request
	// is client-constructed and must fail fast with an actionable error even
	// when no daemon is available (see TestNATSDaemonRouter_OversizeRequest_FailsFast).
	data, reqID, err := r.buildDaemonCommand(ctx, commandType, payload, timeoutMs)
	if err != nil {
		return nil, err
	}
	// Default routing: resolve the user's daemon and pin the command to it, so
	// resolution and delivery use the same daemon id (a caller that also records
	// which daemon ran the command can resolve once and reuse SendDaemonCommandToDaemon).
	daemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("resolving daemon for command: %w", err)
	}
	return r.sendDaemonCommandData(ctx, userID, daemonID, commandType, data, reqID, timeoutMs)
}

// SendDaemonCommandToDaemon sends a generic command to a SPECIFIC daemon id,
// bypassing default resolution. Use when the target daemon is already known
// (e.g. every worktree.create for one worktree must hit the same daemon that
// will own it on disk) so the operation and any recorded owner id agree.
func (r *NATSDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID string, daemonID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	data, reqID, err := r.buildDaemonCommand(ctx, commandType, payload, timeoutMs)
	if err != nil {
		return nil, err
	}
	return r.sendDaemonCommandData(ctx, userID, daemonID, commandType, data, reqID, timeoutMs)
}

// buildDaemonCommand marshals a daemon command envelope and runs the oversize
// preflight. Split out so both the default-routing and daemon-pinned entry
// points run the client-side size check before any daemon resolution.
func (r *NATSDaemonRouter) buildDaemonCommand(ctx context.Context, commandType string, payload []byte, timeoutMs int32) (data []byte, requestID string, err error) {
	requestID = newRequestID()
	req := struct {
		RequestID   string                   `json:"request_id"`
		CommandType string                   `json:"command_type"`
		Payload     json.RawMessage          `json:"payload"`
		TimeoutMs   int32                    `json:"timeout_ms"`
		Policy      *daemonpolicy.WirePolicy `json:"policy,omitempty"`
	}{
		RequestID:   requestID,
		CommandType: commandType,
		Payload:     json.RawMessage(payload),
		TimeoutMs:   timeoutMs,
		// Connector callers carry a confinement policy in context; first-party
		// callers carry none and the field is omitted, leaving the daemon's
		// behavior unchanged. Reading it from context rather than taking it as
		// a parameter means an intermediate DaemonRouter implementation cannot
		// silently drop it — which is exactly how the enforcement gate came to
		// be dead code the first time this was wired.
		Policy: daemonpolicy.ToWire(daemonpolicy.FromContext(ctx)),
	}

	data, err = json.Marshal(req)
	if err != nil {
		return nil, "", fmt.Errorf("marshal daemon command: %w", err)
	}

	// A request over the connection's max_payload is CHUNKED, not rejected —
	// see nats_chunked_request.go. Only the absolute per-request cap is a
	// hard failure, and preflighting it here means a genuinely impossible
	// request fails before daemon resolution and before anything hits the
	// wire, rather than after publishing half a stream.
	if len(data) > maxChunkedRequestBytes {
		observability.NATSErrorsTotal.WithLabelValues("daemon.command", "oversize_request").Inc()
		return nil, "", fmt.Errorf("daemon command %s: %s", commandType,
			oversizeNATSPayloadError("request", len(data), maxChunkedRequestBytes, oversizeRequestHint))
	}
	return data, requestID, nil
}

// sendDaemonCommandData performs the NATS round trip for a pre-marshalled,
// pre-checked daemon command envelope addressed to a specific daemon.
func (r *NATSDaemonRouter) sendDaemonCommandData(ctx context.Context, userID, daemonID, commandType string, data []byte, requestID string, timeoutMs int32) ([]byte, error) {
	timeout := 30 * time.Second
	if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}

	// Respect the caller's context deadline if it's sooner than the explicit timeout.
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	// Run NATS request in a goroutine so we can also select on ctx.Done().
	type natsResult struct {
		msg *nats.Msg
		err error
	}
	subject := daemonSubject(daemonCommandSubject, userID, daemonID)
	reqMsg := observability.NATSPublishMsg(ctx, subject, data)
	resultCh := make(chan natsResult, 1)
	start := time.Now()
	go func() {
		// Chunk-aware request: transparently reassembles oversize replies
		msg, err := requestWithChunkedReply(r.nc, reqMsg, timeout)
		resultCh <- natsResult{msg, err}
	}()

	var msg *nats.Msg
	select {
	case res := <-resultCh:
		observability.NATSRequestDuration.WithLabelValues("daemon.command").Observe(time.Since(start).Seconds())
		if res.err != nil {
			observability.NATSErrorsTotal.WithLabelValues("daemon.command", "request").Inc()
			// Self-describing failure: the subject names the exact daemon the
			// command was addressed to, which is the first thing to check when
			// a command times out (stale daemon resolution vs. dead stream) —
			// the 2026-07-09 git_changes timeouts were undiagnosable from the
			// bare "nats: timeout" alone.
			logging.Warn("[DaemonRouter] daemon command failed",
				"subject", subject, "commandType", commandType,
				"payloadBytes", len(data), "timeout", timeout.String(),
				"elapsed", time.Since(start).Round(time.Millisecond).String(),
				"error", res.err)
			return nil, daemonRequestError(
				fmt.Sprintf("daemon command %s (subject %s, timeout %s)", commandType, subject, timeout), res.err)
		}
		msg = res.msg
	case <-ctx.Done():
		_ = r.sendCancelToDaemon(context.Background(), userID, daemonID, requestID, "daemon command caller cancelled")
		observability.NATSErrorsTotal.WithLabelValues("daemon.command", "timeout").Inc()
		return nil, fmt.Errorf("daemon command via NATS failed: %w", ctx.Err())
	}

	var resp struct {
		Success      bool   `json:"success"`
		Payload      []byte `json:"payload"`
		ErrorMessage string `json:"error_message,omitempty"`
	}
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal daemon command response: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("daemon command %q failed: %s", commandType, resp.ErrorMessage)
	}
	return resp.Payload, nil
}

func (r *NATSDaemonRouter) SendToolRequestSync(ctx context.Context, userID string, request *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal tool request: %w", err)
	}

	// Only the absolute cap rejects — anything between max_payload and the
	// cap is chunked. See buildDaemonCommand for the rationale.
	if len(payload) > maxChunkedRequestBytes {
		observability.NATSErrorsTotal.WithLabelValues("tools.request.sync", "oversize_request").Inc()
		return nil, fmt.Errorf("tool request %s: %s", request.ToolName,
			oversizeNATSPayloadError("request", len(payload), maxChunkedRequestBytes, oversizeRequestHint))
	}

	timeout := 10 * time.Minute
	if request.TimeoutMs > 0 {
		timeout = time.Duration(request.TimeoutMs)*time.Millisecond + 30*time.Second // buffer for daemon overhead
	}

	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	type natsResult struct {
		msg *nats.Msg
		err error
	}
	resolvedDaemonID, err := r.resolveDefaultDaemonID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("resolving daemon for sync request: %w", err)
	}
	subject := daemonSubject(toolRequestSyncSubject, userID, resolvedDaemonID)
	reqMsg := observability.NATSPublishMsg(ctx, subject, payload)
	resultCh := make(chan natsResult, 1)
	start := time.Now()
	forget := r.inflight.record(userID, resolvedDaemonID, request.RequestID, request.ToolCallID)
	go func() {
		// Chunk-aware request: transparently reassembles oversize replies
		msg, err := requestWithChunkedReply(r.nc, reqMsg, timeout)
		resultCh <- natsResult{msg, err}
	}()

	var msg *nats.Msg
	select {
	case res := <-resultCh:
		if res.err == nil {
			forget()
		}
		observability.NATSRequestDuration.WithLabelValues("tools.request.sync").Observe(time.Since(start).Seconds())
		if res.err != nil {
			observability.NATSErrorsTotal.WithLabelValues("tools.request.sync", "request").Inc()
			return nil, daemonRequestError("tool request", res.err)
		}
		msg = res.msg
	case <-ctx.Done():
		observability.NATSErrorsTotal.WithLabelValues("tools.request.sync", "timeout").Inc()
		return nil, fmt.Errorf("tool request via NATS failed: %w", ctx.Err())
	}

	var resp ToolExecutionResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal tool response: %w", err)
	}
	return &resp, nil
}

func (r *NATSDaemonRouter) SendToolRequestSyncWithSelector(ctx context.Context, userID string, request *ToolExecutionRequest, selector *DaemonSelector) (*ToolExecutionResponse, error) {
	if selector == nil {
		return r.SendToolRequestSync(ctx, userID, request)
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal tool request: %w", err)
	}

	// Only the absolute cap rejects — see buildDaemonCommand.
	if len(payload) > maxChunkedRequestBytes {
		observability.NATSErrorsTotal.WithLabelValues("tools.request.sync.selector", "oversize_request").Inc()
		return nil, fmt.Errorf("tool request %s: %s", request.ToolName,
			oversizeNATSPayloadError("request", len(payload), maxChunkedRequestBytes, oversizeRequestHint))
	}

	timeout := 10 * time.Minute
	if request.TimeoutMs > 0 {
		timeout = time.Duration(request.TimeoutMs)*time.Millisecond + 30*time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}

	resolvedDaemonID, err := r.resolveDaemonID(ctx, userID, selector)
	if err != nil {
		return nil, fmt.Errorf("resolving daemon for selector: %w", err)
	}

	type natsResult struct {
		msg *nats.Msg
		err error
	}
	subject := daemonSubject(toolRequestSyncSubject, userID, resolvedDaemonID)
	reqMsg := observability.NATSPublishMsg(ctx, subject, payload)
	resultCh := make(chan natsResult, 1)
	start := time.Now()
	forget := r.inflight.record(userID, resolvedDaemonID, request.RequestID, request.ToolCallID)
	go func() {
		// Chunk-aware request: transparently reassembles oversize replies
		msg, err := requestWithChunkedReply(r.nc, reqMsg, timeout)
		resultCh <- natsResult{msg, err}
	}()

	var msg *nats.Msg
	select {
	case res := <-resultCh:
		if res.err == nil {
			forget()
		}
		observability.NATSRequestDuration.WithLabelValues("tools.request.sync.selector").Observe(time.Since(start).Seconds())
		if res.err != nil {
			observability.NATSErrorsTotal.WithLabelValues("tools.request.sync.selector", "request").Inc()
			return nil, daemonRequestError("tool request", res.err)
		}
		msg = res.msg
	case <-ctx.Done():
		observability.NATSErrorsTotal.WithLabelValues("tools.request.sync.selector", "timeout").Inc()
		return nil, fmt.Errorf("tool request via NATS failed: %w", ctx.Err())
	}

	var resp ToolExecutionResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal tool response: %w", err)
	}
	return &resp, nil
}

func (r *NATSDaemonRouter) SendLoadProjectConfigs(ctx context.Context, userID string, projectPath string, requestID string) error {
	payload, err := json.Marshal(map[string]string{
		"project_path": projectPath,
		"request_id":   requestID,
	})
	if err != nil {
		return err
	}
	resolvedDaemonID, err := r.resolveFollowUpDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for config load: %w", err)
	}
	subject := daemonSubject(configLoadSubject, userID, resolvedDaemonID)
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.config.load", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.config.load").Inc()
	return nil
}

func (r *NATSDaemonRouter) SendWatchProjectConfigs(ctx context.Context, userID string, projectPath string, includeInitial bool) error {
	payload, err := json.Marshal(map[string]interface{}{
		"project_path":    projectPath,
		"include_initial": includeInitial,
	})
	if err != nil {
		return err
	}
	resolvedDaemonID, err := r.resolveFollowUpDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for config watch: %w", err)
	}
	subject := daemonSubject(configWatchSubject, userID, resolvedDaemonID)
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.config.watch", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.config.watch").Inc()
	return nil
}

func (r *NATSDaemonRouter) SendTerminalInput(ctx context.Context, userID string, sessionID string, data []byte) error {
	payload, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
		Data      []byte `json:"data"`
	}{SessionID: sessionID, Data: data})
	if err != nil {
		return err
	}
	resolvedDaemonID, err := r.resolveFollowUpDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for terminal input: %w", err)
	}
	subject := daemonSubject(terminalInputSubject, userID, resolvedDaemonID) + "." + sessionID
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.terminal.input", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.terminal.input").Inc()
	return nil
}

func (r *NATSDaemonRouter) SendTerminalResize(ctx context.Context, userID string, sessionID string, cols, rows uint32) error {
	payload, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
		Cols      uint32 `json:"cols"`
		Rows      uint32 `json:"rows"`
	}{SessionID: sessionID, Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	resolvedDaemonID, err := r.resolveFollowUpDaemonID(ctx, userID)
	if err != nil {
		return fmt.Errorf("resolving daemon for terminal resize: %w", err)
	}
	subject := daemonSubject(terminalResizeSubject, userID, resolvedDaemonID) + "." + sessionID
	msg := observability.NATSPublishMsg(ctx, subject, payload)
	if err := r.nc.PublishMsg(msg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.terminal.resize", "publish").Inc()
		return err
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.terminal.resize").Inc()
	return nil
}

func (r *NATSDaemonRouter) SubscribeTerminalOutput(ctx context.Context, userID string, sessionID string) (<-chan *TerminalOutputEvent, func(), error) {
	resolvedDaemonID, err := r.resolveFollowUpDaemonID(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving daemon for terminal output: %w", err)
	}
	ch := make(chan *TerminalOutputEvent, 64)
	subject := daemonSubject(terminalOutputSubject, userID, resolvedDaemonID) + "." + sessionID

	sub, err := r.nc.Subscribe(subject, func(msg *nats.Msg) {
		var evt TerminalOutputEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			return
		}
		select {
		case ch <- &evt:
		default:
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("subscribe to terminal output via NATS: %w", err)
	}

	// Only AFTER the local NATS subscription is live do we publish the subscribe
	// request down to the bridge. The bridge starts the terminal output
	// forwarder, which starts the daemon's PTY pump — so the daemon does not read
	// the PTY until the full WS->NATS->bridge->daemon interest chain is
	// established and the initial shell prompt can no longer be dropped. Mirrors
	// SubscribeProcessOutput, but with the publish deliberately ordered after the
	// subscribe (the whole point of the terminal fix).
	reqPayload, err := json.Marshal(struct {
		SessionID string `json:"session_id"`
	}{SessionID: sessionID})
	if err != nil {
		_ = sub.Unsubscribe()
		return nil, nil, err
	}
	subscribeSubject := daemonSubject(terminalSubscribeSubject, userID, resolvedDaemonID) + "." + sessionID
	subMsg := observability.NATSPublishMsg(ctx, subscribeSubject, reqPayload)
	if err := r.nc.PublishMsg(subMsg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.terminal.subscribe", "publish").Inc()
		_ = sub.Unsubscribe()
		return nil, nil, fmt.Errorf("publish terminal output subscribe request via NATS: %w", err)
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.terminal.subscribe").Inc()

	unsub := func() {
		_ = sub.Unsubscribe()
	}
	return ch, unsub, nil
}

func (r *NATSDaemonRouter) SubscribeProcessOutput(ctx context.Context, userID string, processID string, newOnly bool) (<-chan *ProcessOutputEvent, func(), error) {
	// Publish subscribe request so the bridge/gateway knows to start forwarding.
	reqPayload, err := json.Marshal(struct {
		ProcessID string `json:"process_id"`
		NewOnly   bool   `json:"new_only"`
	}{ProcessID: processID, NewOnly: newOnly})
	if err != nil {
		return nil, nil, err
	}
	resolvedDaemonID, err := r.resolveFollowUpDaemonID(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving daemon for process subscribe: %w", err)
	}
	subject := daemonSubject(processOutputSubscribeSubject, userID, resolvedDaemonID)
	subMsg := observability.NATSPublishMsg(ctx, subject, reqPayload)
	if err := r.nc.PublishMsg(subMsg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.process.subscribe", "publish").Inc()
		return nil, nil, fmt.Errorf("publish process output subscribe request via NATS: %w", err)
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.process.subscribe").Inc()

	ch := make(chan *ProcessOutputEvent, 128)
	subject = processOutputSubject + "." + userID + "." + processID

	sub, err := r.nc.Subscribe(subject, func(msg *nats.Msg) {
		var evt ProcessOutputEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			return
		}
		select {
		case ch <- &evt:
		default:
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("subscribe to process output via NATS: %w", err)
	}

	unsub := func() {
		_ = sub.Unsubscribe()
	}
	return ch, unsub, nil
}

func (r *NATSDaemonRouter) Close() error {
	return nil
}

// pendingCommandsStream and pendingSubjectPrefix mirror the values used by
// control-plane's natsio package (StreamDaemonPendingCommands /
// SubjectDaemonPendingAll) and by the gateway's NATSToolBridge drainer.
// Kept as constants here so we don't take a circular dep on control-plane.
const (
	pendingCommandsStream = "DAEMON_PENDING_COMMANDS"
	pendingSubjectPrefix  = "daemon.pending."
)

// EnqueueDaemonCommand persists a fire-and-forget command to
// DAEMON_PENDING_COMMANDS for every daemon the user owns. The gateway's
// NATSToolBridge.drainPendingCommands consumer picks each message up on
// the daemon's next connect and dispatches it via SendDaemonCommand,
// healing creation-time races where the daemon wasn't yet online.
func (r *NATSDaemonRouter) EnqueueDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) (int, error) {
	if userID == "" {
		return 0, fmt.Errorf("userID required")
	}
	if r.db == nil {
		return 0, fmt.Errorf("daemon router has no DB; cannot resolve user's daemons for enqueue")
	}

	daemons, err := r.db.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("listing daemons for enqueue: %w", err)
	}
	if len(daemons) == 0 {
		return 0, nil
	}

	js, err := r.jetstream()
	if err != nil {
		return 0, err
	}

	envelope, err := json.Marshal(struct {
		RequestID   string                   `json:"request_id"`
		CommandType string                   `json:"command_type"`
		Payload     json.RawMessage          `json:"payload"`
		TimeoutMs   int32                    `json:"timeout_ms"`
		Policy      *daemonpolicy.WirePolicy `json:"policy,omitempty"`
	}{
		RequestID:   "enq-" + newRequestID(),
		CommandType: commandType,
		Payload:     json.RawMessage(payload),
		TimeoutMs:   timeoutMs,
		// Carried even though no connector path enqueues today: the drain side
		// already decodes it, and an enqueued command that replayed
		// unrestricted would be a policy bypass with a delay fuse. Cheaper to
		// close now than to remember later.
		Policy: daemonpolicy.ToWire(daemonpolicy.FromContext(ctx)),
	})
	if err != nil {
		return 0, fmt.Errorf("marshal pending command envelope: %w", err)
	}

	var enqueued int
	for _, d := range daemons {
		if d == nil || d.ID == "" {
			continue
		}
		// Sanitize the daemonID to keep this subject identical to the one the
		// gateway's drainer filters on (see sanitizePendingSubjectToken) and to
		// control-plane's natsio.SanitizeSubject. A raw daemonID with a '.',
		// '>', '*' or ' ' would publish to a subject the consumer never matches.
		subject := pendingSubjectPrefix + sanitizePendingSubjectToken(d.ID)
		if _, pubErr := js.Publish(ctx, subject, envelope); pubErr != nil {
			logging.Warn("EnqueueDaemonCommand: JetStream publish failed",
				"userID", userID, "daemonID", d.ID, "subject", subject,
				"commandType", commandType, "error", pubErr)
			continue
		}
		enqueued++
	}
	return enqueued, nil
}

// jetstream returns the lazily-initialized JetStream context. Safe to call
// from multiple goroutines; sync.Once guarantees a single init.
func (r *NATSDaemonRouter) jetstream() (jetstream.JetStream, error) {
	r.jsOnce.Do(func() {
		if r.nc == nil {
			r.jsErr = fmt.Errorf("nats connection is nil")
			return
		}
		r.js, r.jsErr = jetstream.New(r.nc)
	})
	return r.js, r.jsErr
}

// Compile-time interface check.
var _ DaemonRouter = (*NATSDaemonRouter)(nil)
