// Copyright (c) 2025 Reliant Labs

package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonactivity"
	"github.com/reliant-labs/reliant/internal/daemonevents"
	"github.com/reliant-labs/reliant/internal/daemonquery"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	"github.com/reliant-labs/reliant/internal/db"
	grpcserver "github.com/reliant-labs/reliant/internal/grpc"
	"github.com/reliant-labs/reliant/internal/handlers/tools_daemon"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/servergateway"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/config"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

// The daemon gateway's nodes (the gateway root, roots.go). Most adapt an
// existing component to the graph's lifecycle signatures — Run/Close taking a
// context and returning an error — without changing it; the rest are the
// gateway's own wiring: what each piece registers with ToolsDaemonService.

// DaemonAuthDeps is what resolves daemon credentials.
type DaemonAuthDeps struct {
	Tokens auth.AccessTokenIntrospector
}

// NewDaemonAuth authenticates daemons by their `rlat_` access token, checked
// once per connect against the token authority, uncached. It admits the
// daemon audience, and only it: ToolsDaemonService declares that audience, and
// `forge lint --graph` and the listener's boot check prove the daemon listener
// serves nothing else.
//
//forge:audience daemon
func NewDaemonAuth(d DaemonAuthDeps) (*grpcserver.DaemonAuth, error) {
	return grpcserver.NewDaemonAuth(d.Tokens)
}

// DaemonChainDeps is the daemon chain's authenticator.
type DaemonChainDeps struct {
	Auth   *grpcserver.DaemonAuth
	Logger *slog.Logger
}

// NewDaemonChain is the daemon listener's interceptor chain
// (grpcserver.NewDaemonChain says where it departs from forge's defaults).
func NewDaemonChain(d DaemonChainDeps) (*grpcserver.DaemonChain, error) {
	return grpcserver.NewDaemonChain(grpcserver.DaemonChainDeps{Auth: d.Auth, Logger: d.Logger})
}

// DaemonListenerDeps is where daemons dial and what they reach there.
type DaemonListenerDeps struct {
	Gateway *config.GatewayConfig
	TLS     *TLSFiles
	Chain   *grpcserver.DaemonChain
	Routes  *DaemonRoutes
	Logger  *slog.Logger
}

// NewDaemonListener is the address daemons dial (TOOLS_DAEMON_PORT on
// BIND_ADDRESS): the daemon-audience routes through the daemon chain. Its
// drain is the daemon hand-over (grpcserver.NewDaemonListener).
func NewDaemonListener(d DaemonListenerDeps) (*grpcserver.DaemonListener, error) {
	return grpcserver.NewDaemonListener(grpcserver.DaemonListenerDeps{
		Port:        int(d.Gateway.GetDaemonPort()),
		BindAddress: d.Gateway.GetBindAddress(),
		TLSCertFile: d.TLS.CertFile,
		TLSKeyFile:  d.TLS.KeyFile,
		Chain:       d.Chain,
		Routes:      d.Routes,
		Logger:      d.Logger,
	})
}

// DaemonStatePublisherDeps is the connection lifecycle events go out on.
type DaemonStatePublisherDeps struct {
	NATS *NATSConn
}

// NewDaemonStatePublisher emits daemon.v1.state.* lifecycle events on the
// core-NATS bus. The control-plane derivation consumer fans these out to the
// derived stores (daemon_attachment, daemons.status, the workspace CR).
func NewDaemonStatePublisher(d DaemonStatePublisherDeps) (*daemonstate.Publisher, error) {
	return daemonstate.NewPublisher(d.NATS.Conn), nil
}

// ToolResultPublisherDeps is the connection tool results go out on.
type ToolResultPublisherDeps struct {
	NATS *NATSConn
}

// NewToolResultPublisher delivers a tool result whose request reached the
// daemon through another replica (or an earlier stream) to the worker waiting
// on the request's result subject. This is what lets a call survive a gateway
// rollout.
func NewToolResultPublisher(d ToolResultPublisherDeps) (*toolexec.ToolResultPublisher, error) {
	return toolexec.NewToolResultPublisher(d.NATS.Conn), nil
}

// ToolsDaemonSweeper runs ToolsDaemonService's stale-connection sweeper and,
// on stop, ends what the service still holds. It is the gateway's last node
// to stop: everything else that reaches the service stops first.
type ToolsDaemonSweeper struct{ svc *tools_daemon.Service }

// ToolsDaemonSweeperDeps is the service it sweeps.
type ToolsDaemonSweeperDeps struct {
	ToolsDaemon *tools_daemon.Service
}

// NewToolsDaemonSweeper builds the sweeper node.
func NewToolsDaemonSweeper(d ToolsDaemonSweeperDeps) (*ToolsDaemonSweeper, error) {
	return &ToolsDaemonSweeper{svc: d.ToolsDaemon}, nil
}

// Run starts the sweeper, which nukes connections whose application-level
// stream has died while the TCP socket stays ESTABLISHED (observed in
// production).
func (s *ToolsDaemonSweeper) Run(ctx context.Context) error {
	s.svc.Start(ctx)
	return nil
}

// Close ends anything the service still holds (an outbound connection), and
// stops the sweeper.
func (s *ToolsDaemonSweeper) Close(context.Context) error {
	s.svc.Close()
	return nil
}

// StateDerivation is the reliant-side derivation consumer: it mirrors the
// control plane's lifecycle, removal and registry-snapshot events onto the
// daemons table and reaps expired attachment leases. Liveness
// (daemon_attachment) is not its to write: ToolsDaemonService writes each
// stream's lease itself, in stream order.
type StateDerivation struct{ d *daemonstate.Derivation }

// StateDerivationDeps is what it consumes and writes.
type StateDerivationDeps struct {
	NATS        *NATSConn
	DB          *db.Repo
	ToolsDaemon *tools_daemon.Service
}

// NewStateDerivation builds the consumer. A control-plane lifecycle
// transition (provisioning → running, suspend, an OOM kill) changes what
// ListDaemons reports, so it tells the owner's web clients to refetch rather
// than leaving them to poll for it.
func NewStateDerivation(d StateDerivationDeps) (*StateDerivation, error) {
	derivation := daemonstate.NewDerivation(d.NATS.Conn, d.DB)
	derivation.NotifyLifecycleApplied(d.ToolsDaemon.PublishDaemonListChanged)
	return &StateDerivation{d: derivation}, nil
}

// Run consumes until ctx ends. A failure is logged, not fatal: the daemon
// streams do not depend on it.
func (s *StateDerivation) Run(ctx context.Context) error {
	logging.InfoContext(ctx, "Daemon state derivation consumer started", "subject", daemonwire.SubjectWildcard)
	if err := s.d.Start(ctx); err != nil && ctx.Err() == nil {
		logging.ErrorContext(ctx, "Daemon state derivation consumer failed", "error", err)
	}
	return nil
}

// ToolBridge is the NATS tool bridge: per-daemon subscriptions, created and
// destroyed as daemons connect and disconnect, forwarding tool execution
// requests from api-server replicas and Temporal workers to the stream.
type ToolBridge struct{ b *toolexec.NATSToolBridge }

// ToolBridgeDeps is what the bridge routes between.
type ToolBridgeDeps struct {
	NATS        *NATSConn
	JetStream   jetstream.JetStream
	ToolsDaemon *tools_daemon.Service
}

// NewToolBridge builds and starts the bridge, registered as ToolsDaemonService's
// FIRST connection listener: a daemon's routes exist before anything else
// hears it connected (ConnectionListeners registers the rest after it).
func NewToolBridge(d ToolBridgeDeps) (*ToolBridge, error) {
	bridge := toolexec.NewNATSToolBridge(d.NATS.Conn, d.JetStream, d.ToolsDaemon)
	d.ToolsDaemon.AddConnectionListener(bridge)
	if err := bridge.Start(); err != nil {
		return nil, fmt.Errorf("failed to start NATS tool bridge: %w", err)
	}
	logging.Info("NATS tool bridge started — per-daemon subscriptions on connect/disconnect")
	return &ToolBridge{b: bridge}, nil
}

// Close drops the bridge's subscriptions, once no daemon stream is left to
// route to.
func (t *ToolBridge) Close(context.Context) error { return t.b.Close() }

// DaemonEventPublisherDeps is the stream daemon lifecycle events go to.
type DaemonEventPublisherDeps struct {
	Ctx       context.Context
	NATS      *NATSConn
	JetStream jetstream.JetStream
}

// NewDaemonEventPublisher forwards daemon connect/disconnect (and the
// connector's connect failures) to the control-plane admin-server through the
// DAEMON_EVENTS JetStream stream, so controlplane.daemons.status stays in sync
// with reality. It ensures the stream first, within 10s.
func NewDaemonEventPublisher(d DaemonEventPublisherDeps) (*daemonevents.Publisher, error) {
	ctx, cancel := context.WithTimeout(d.Ctx, 10*time.Second)
	defer cancel()
	if err := daemonevents.EnsureStream(ctx, d.JetStream); err != nil {
		return nil, fmt.Errorf("failed to ensure %s stream: %w", daemonevents.StreamDaemonEvents, err)
	}
	publisher, err := daemonevents.NewPublisher(d.NATS.Conn)
	if err != nil {
		return nil, fmt.Errorf("failed to build daemon event publisher: %w", err)
	}
	return publisher, nil
}

// ConnectionListeners is everything else that hears ToolsDaemonService's
// daemons connect and disconnect, registered after the tool bridge and in
// this order: the lifecycle publisher, the lifecycle event publisher, the
// status responder, the per-user liveness responder.
type ConnectionListeners struct {
	// Registered is each listener in the order it was registered.
	Registered []toolexec.DaemonConnectionListener
}

// ConnectionListenersDeps is what the listeners publish on and answer from.
// Bridge is not used: it orders this node after the tool bridge, whose
// registration comes first.
type ConnectionListenersDeps struct {
	NATS        *NATSConn
	ToolsDaemon *tools_daemon.Service
	Bridge      *ToolBridge
	Events      *daemonevents.Publisher
}

// NewConnectionListeners registers the gateway's connection listeners.
func NewConnectionListeners(d ConnectionListenersDeps) (*ConnectionListeners, error) {
	return registerConnectionListeners(d.ToolsDaemon, d.NATS.Conn, d.ToolsDaemon, d.Events), nil
}

// connectionRegistrar is where connection listeners register:
// ToolsDaemonService.
type connectionRegistrar interface {
	AddConnectionListener(toolexec.DaemonConnectionListener)
}

// daemonSource is what the responders answer from: ToolsDaemonService's
// in-memory connection map.
type daemonSource interface {
	daemonquery.StatusSource
	daemonquery.UserLivenessSource
}

// registerConnectionListeners builds and registers, in order:
//
//  1. the lifecycle publisher, for the in-cluster subscribers of daemon
//     connect/disconnect;
//  2. events, the DAEMON_EVENTS publisher;
//  3. the pull-architecture status responder: subscribes per daemon to
//     daemon.v1.query.<daemonID>.status when a connection is registered, and
//     answers from in-memory state, so the control plane's request reaches
//     whichever gateway holds the stream — no service registry, no DB column
//     to keep in sync;
//  4. the per-user any-live responder: subscribes to
//     daemon.v1.query.user.<userID>.any-live when a user's first daemon
//     connects here and unsubscribes when their last one disconnects, giving
//     daemonliveness.ReachableByUser the same NATS-first pull path.
func registerConnectionListeners(reg connectionRegistrar, nc *nats.Conn, src daemonSource, events *daemonevents.Publisher) *ConnectionListeners {
	out := &ConnectionListeners{}
	add := func(l toolexec.DaemonConnectionListener) {
		reg.AddConnectionListener(l)
		out.Registered = append(out.Registered, l)
	}
	add(toolexec.NewDaemonLifecyclePublisher(nc))
	add(events)
	logging.Info("Daemon lifecycle event publisher started",
		"stream", daemonevents.StreamDaemonEvents,
		"subjects", daemonevents.SubjectDaemonEventsAll,
	)
	add(daemonquery.NewResponder(nc, src))
	logging.Info("Daemon status responder started",
		"subjectPattern", daemonquery.SubjectQueryPrefix+"<daemonID>.status",
	)
	add(daemonquery.NewUserResponder(nc, src))
	logging.Info("Per-user daemon liveness responder started",
		"subjectPattern", daemonquery.SubjectQueryPrefix+"user.<userID>.any-live",
	)
	return out
}

// ActivitySubscriber is the cross-process activity ping subscriber: the
// control-plane LLM proxy publishes to daemon.v1.activity.user.<userID> when
// forwarding a call, and it bumps lastActivity for that user's daemons so the
// status query reflects real usage between heartbeats.
type ActivitySubscriber struct{ s *daemonactivity.Subscriber }

// ActivitySubscriberDeps is the connection it subscribes on and the service
// whose daemons it bumps.
type ActivitySubscriberDeps struct {
	NATS        *NATSConn
	ToolsDaemon *tools_daemon.Service
}

// NewActivitySubscriber builds and starts the subscriber.
func NewActivitySubscriber(d ActivitySubscriberDeps) (*ActivitySubscriber, error) {
	s := daemonactivity.NewSubscriber()
	if err := s.Start(d.NATS.Conn, d.ToolsDaemon); err != nil {
		return nil, fmt.Errorf("failed to start daemon activity subscriber: %w", err)
	}
	logging.Info("Daemon activity subscriber started", "subjectPattern", daemonactivity.SubjectWildcard)
	return &ActivitySubscriber{s: s}, nil
}

// Close unsubscribes.
func (a *ActivitySubscriber) Close(context.Context) error {
	a.s.Stop()
	return nil
}

// DaemonConnectorDeps is the service outbound connections register with and
// where their connect failures are reported.
type DaemonConnectorDeps struct {
	ToolsDaemon *tools_daemon.Service
	Events      *daemonevents.Publisher
}

// NewDaemonConnector is the outbound gateway→daemon dial engine the managed
// reconciler drives. It reports connect failures through the same publisher
// as connected/disconnected, so the control plane can persist a reason on the
// daemon row.
func NewDaemonConnector(d DaemonConnectorDeps) (*servergateway.DaemonConnector, error) {
	return servergateway.NewDaemonConnector(d.ToolsDaemon, d.Events), nil
}

// ManagedReconciler runs the managed-daemon reconciler, which dials the
// managed daemons this replica should hold outbound.
type ManagedReconciler struct {
	r         *servergateway.ManagedDaemonReconciler
	connector *servergateway.DaemonConnector
}

// ManagedReconcilerDeps is where it learns which daemons to dial, and the
// connector it dials them with.
type ManagedReconcilerDeps struct {
	JetStream jetstream.JetStream
	Connector *servergateway.DaemonConnector
}

// NewManagedReconciler builds the reconciler node.
func NewManagedReconciler(d ManagedReconcilerDeps) (*ManagedReconciler, error) {
	return &ManagedReconciler{r: servergateway.NewManagedDaemonReconciler(d.JetStream, d.Connector), connector: d.Connector}, nil
}

// Run reconciles until ctx ends. A failure is logged, not fatal: inbound
// daemons do not depend on it.
func (m *ManagedReconciler) Run(ctx context.Context) error {
	logging.InfoContext(ctx, "Managed daemon reconciler started")
	if err := m.r.Start(ctx); err != nil && ctx.Err() == nil {
		logging.ErrorContext(ctx, "ManagedDaemonReconciler failed", "error", err)
	}
	return nil
}

// Close ends every outbound connection, so the connector does not redial a
// daemon this replica is about to drop.
func (m *ManagedReconciler) Close(context.Context) error {
	m.connector.CloseAll()
	return nil
}

// FlowHealth is the gateway's APP-FLOW health endpoint, GET /flow-health on
// the probe surface (servergateway.FlowHealthHandler): the daemon-connected
// invariant, asserted from the registry (the repo) and this process's live
// connection map, as a status-only 200/503 that `forge smoke` curls.
type FlowHealth struct{ Handler http.Handler }

// FlowHealthDeps is the registry and the connection map it asserts over.
type FlowHealthDeps struct {
	DB          *db.Repo
	ToolsDaemon *tools_daemon.Service
}

// NewFlowHealth builds the handler; its compose method serves it on the probe
// surface.
func NewFlowHealth(d FlowHealthDeps) (*FlowHealth, error) {
	return &FlowHealth{Handler: servergateway.FlowHealthHandler(d.DB, d.ToolsDaemon)}, nil
}
