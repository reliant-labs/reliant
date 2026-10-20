// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run drainPendingCommands against a real 3-node embedded NATS
// cluster holding DAEMON_PENDING_COMMANDS as an R3 WorkQueue stream — the prod
// shape. The prod failure was invisible to the stub-based drain tests: every
// consumer on an R3 stream is its own raft group, and creating/deleting one
// per poll per daemon kept followers "not current", failing /healthz.

type natsTestCluster struct {
	servers []*server.Server
}

func freeLocalPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// routePort picks the port a member's route listener binds. A variable so a
// test can hand out a port that is already taken.
var routePort = freeLocalPort

// errRoutePortTaken: a member could not bind the route port picked for it.
// Every member's routes name every other member's port, so the ports must be
// chosen before any member starts, and freeLocalPort releases its pick for the
// member to bind. In between, another process can take it — an outbound
// connection draws its local port from the same ephemeral range — and that
// member then never listens for routes.
var errRoutePortTaken = errors.New("route port was taken before its member could bind it")

// fatalRecorder is a member's logger. It keeps the first fatal error, which
// nats-server reports only through its logger — a member that cannot bind its
// route port simply never becomes ready — and drops everything else, as NoLog
// did.
type fatalRecorder struct {
	mu    sync.Mutex
	first string
}

func (r *fatalRecorder) Fatalf(format string, v ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.first == "" {
		r.first = fmt.Sprintf(format, v...)
	}
}

func (r *fatalRecorder) fatal() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.first
}

func (*fatalRecorder) Noticef(string, ...any) {}
func (*fatalRecorder) Warnf(string, ...any)   {}
func (*fatalRecorder) Errorf(string, ...any)  {}
func (*fatalRecorder) Debugf(string, ...any)  {}
func (*fatalRecorder) Tracef(string, ...any)  {}

func startNATSCluster(t *testing.T, n int) *natsTestCluster {
	t.Helper()
	// A taken route port is retried on fresh ports; it is the machine being
	// busy, not the cluster misbehaving. Any other startup failure is not.
	const attempts = 3
	var c *natsTestCluster
	var routesPerPeer int
	for attempt := 1; ; attempt++ {
		var err error
		c, routesPerPeer, err = bootNATSCluster(t, n)
		if err == nil {
			break
		}
		if !errors.Is(err, errRoutePortTaken) || attempt == attempts {
			t.Fatalf("NATS cluster did not start (attempt %d of %d): %v", attempt, attempts, err)
		}
		t.Logf("NATS cluster attempt %d: %v; retrying on fresh route ports", attempt, err)
	}
	waitClusterFormed(t, c, n, routesPerPeer)
	return c
}

// bootNATSCluster starts n members routed to each other and waits until each
// is listening. A member that reports a fatal error fails it at once rather
// than at the readiness timeout, with every member it started shut down.
//
// routesPerPeer is how many route connections a member holds to each peer once
// the mesh is complete: one per pool slot plus one per pinned account.
// NewServer fills both defaults into the options it is given.
func bootNATSCluster(t *testing.T, n int) (_ *natsTestCluster, routesPerPeer int, _ error) {
	t.Helper()
	routePorts := make([]int, n)
	for i := range routePorts {
		routePorts[i] = routePort(t)
	}
	c := &natsTestCluster{}
	shutdown := func() {
		for _, s := range c.servers {
			s.Shutdown()
		}
	}
	t.Cleanup(shutdown)
	recorders := make([]*fatalRecorder, n)
	for i := 0; i < n; i++ {
		var routes []string
		for j, p := range routePorts {
			if j != i {
				routes = append(routes, fmt.Sprintf("nats://127.0.0.1:%d", p))
			}
		}
		opts := &server.Options{
			ServerName: fmt.Sprintf("n%d", i),
			Host:       "127.0.0.1",
			Port:       -1,
			HTTPPort:   -1,
			JetStream:  true,
			StoreDir:   t.TempDir(),
			NoLog:      true,
			NoSigs:     true,
		}
		opts.Cluster.Name = "test-cluster"
		opts.Cluster.Host = "127.0.0.1"
		opts.Cluster.Port = routePorts[i]
		opts.Routes = server.RoutesFromStr(joinCSV(routes))
		srv, err := server.NewServer(opts)
		require.NoError(t, err)
		recorders[i] = &fatalRecorder{}
		srv.SetLogger(recorders[i], false, false)
		routesPerPeer = max(opts.Cluster.PoolSize, 1) + len(opts.Cluster.PinnedAccounts)
		go srv.Start()
		c.servers = append(c.servers, srv)
	}
	deadline := time.Now().Add(20 * time.Second)
	for _, s := range c.servers {
		// 50ms, not less: ReadyForConnections sleeps between checks only
		// when given more than 25ms, and spins otherwise.
		for !s.ReadyForConnections(50 * time.Millisecond) {
			for i, rec := range recorders {
				msg := rec.fatal()
				if msg == "" {
					continue
				}
				shutdown()
				if strings.Contains(msg, "Error listening on router port") {
					return nil, 0, fmt.Errorf("%w: %s: %s", errRoutePortTaken, c.servers[i].Name(), msg)
				}
				return nil, 0, fmt.Errorf("%s: %s", c.servers[i].Name(), msg)
			}
			if time.Now().After(deadline) {
				shutdown()
				return nil, 0, fmt.Errorf("cluster member %s not ready after 20s", s.Name())
			}
		}
	}
	return c, routesPerPeer, nil
}

func waitClusterFormed(t *testing.T, c *natsTestCluster, n, routesPerPeer int) {
	t.Helper()
	// Wait for the COMPLETE route mesh, not just a JetStream meta leader.
	//
	// The members start together, so some of their first route dials are
	// refused and the races between explicit and implicit routes end in
	// duplicate routes being closed. That can leave one pool slot between two
	// members empty until the 1s reconnect fills it. JetStream rides the
	// system account's pinned route, so the cluster elects leaders and
	// reports healthy, but the $G interest that slot carries does not cross:
	// a stream leader's subscription never reaches the member the test
	// publishes through ("nats: no response from stream"), and an advisory
	// the test subscribed to is dropped by a member that never heard of the
	// subscription. Captured in a protocol trace: n0's RS+ for the advisory
	// reached n2 only 0.7s later, over the reconnected route, after n2 had
	// already published the consumer-created advisory to nobody.
	// nats-server's own tests wait for exactly this (checkClusterFormed).
	wantRoutes := (n - 1) * routesPerPeer
	require.Eventually(t, func() bool {
		for _, s := range c.servers {
			if s.NumRoutes() != wantRoutes {
				return false
			}
		}
		return true
	}, 20*time.Second, 50*time.Millisecond, "route mesh never completed: want %d routes per member", wantRoutes)
	require.Eventually(t, func() bool {
		leader := false
		for _, s := range c.servers {
			if !s.JetStreamIsCurrent() {
				return false
			}
			leader = leader || s.JetStreamIsLeader()
		}
		return leader
	}, 60*time.Second, 200*time.Millisecond, "JetStream meta layer never formed")
}

func joinCSV(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}

func (c *natsTestCluster) connect(t *testing.T) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(c.servers[0].ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// assertHealthy fails unless every member's /healthz settles at ok.
//
// Settles, not "is ok at this instant": a follower of an idle raft group
// reports "not current, no recent leader contact" once 2 x hbInterval passes
// without an append from its leader, and an idle leader heartbeats on a 1s
// ticker that skips a beat when it sent anything in the last second, so its
// gaps run up to ~2s by design. A single sample can land in that gap on a
// perfectly healthy cluster (captured: a 2.03s gap, 4s after the drain's last
// write). The defect these tests guard, per-poll consumer churn, is pinned
// exactly by the consumer-created advisory counts; health here means the drain
// leaves no lasting damage, which a cluster that stays unhealthy fails.
func (c *natsTestCluster) assertHealthy(t *testing.T, when string) {
	t.Helper()
	var last string
	healthy := func() bool {
		for _, s := range c.servers {
			if hs := s.Healthz(&server.HealthzOptions{}); hs.Status != "ok" {
				last = fmt.Sprintf("%s /healthz = %s (%s)", s.Name(), hs.Status, hs.Error)
				return false
			}
		}
		return true
	}
	if healthy() {
		return
	}
	require.Eventually(t, healthy, 10*time.Second, 50*time.Millisecond, "%s: never healthy, last: %s", when, last)
}

// waitInterest blocks until every member has interest in subject, so a
// message published on it from any member reaches the subscriber.
func (c *natsTestCluster) waitInterest(t *testing.T, subject string) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, s := range c.servers {
			if !s.GlobalAccount().SubscriptionInterest(subject) {
				return false
			}
		}
		return true
	}, 10*time.Second, 20*time.Millisecond, "interest in %s never reached every member", subject)
}

func (c *natsTestCluster) waitHealthy(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, s := range c.servers {
			if s.Healthz(&server.HealthzOptions{}).Status != "ok" {
				return false
			}
		}
		return true
	}, 60*time.Second, 200*time.Millisecond, "cluster never became healthy")
}

// pendingStreamConfig mirrors control-plane's pendingCommandsStreamConfig at R3.
func pendingStreamConfig() jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:                 pendingCommandsStream,
		Subjects:             []string{"daemon.pending.>"},
		Retention:            jetstream.WorkQueuePolicy,
		Storage:              jetstream.FileStorage,
		MaxMsgsPerSubject:    100,
		Discard:              jetstream.DiscardNew,
		DiscardNewPerSubject: true,
		Replicas:             3,
	}
}

type pendingClusterEnv struct {
	cluster *natsTestCluster
	nc      *nats.Conn
	js      jetstream.JetStream
	stream  jetstream.Stream
	// consumerCreates counts consumer-created advisories on the stream.
	consumerCreates atomic.Int64
}

func newPendingClusterEnv(t *testing.T) *pendingClusterEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: boots a 3-node NATS cluster; runs in the full lane")
	}
	c := startNATSCluster(t, 3)
	nc := c.connect(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)

	var stream jetstream.Stream
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stream, err = js.CreateOrUpdateStream(ctx, pendingStreamConfig())
		return err == nil
	}, 60*time.Second, 250*time.Millisecond, "create R3 stream: %v", err)
	require.Equal(t, 3, stream.CachedInfo().Config.Replicas)

	env := &pendingClusterEnv{cluster: c, nc: nc, js: js, stream: stream}
	advisories := "$JS.EVENT.ADVISORY.CONSUMER.CREATED." + pendingCommandsStream + ".*"
	sub, err := nc.Subscribe(advisories, func(*nats.Msg) {
		env.consumerCreates.Add(1)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, nc.Flush())
	// The advisory is published by whichever member leads the new consumer,
	// and dropped if that member has not yet heard of this subscription. Flush
	// proves only that the member we are connected to has it.
	c.waitInterest(t, "$JS.EVENT.ADVISORY.CONSUMER.CREATED."+pendingCommandsStream+".probe")
	// And the stream leader's subscription must have reached the member we
	// publish through, or a publish finds no responders.
	c.waitInterest(t, pendingSubjectPrefix+"probe")
	// A freshly created R3 stream is briefly "not current" on followers; that
	// settles on its own. Baseline AFTER it does, so any later failure is
	// attributable to what the test does.
	c.waitHealthy(t)
	return env
}

func (e *pendingClusterEnv) publish(t *testing.T, daemonID, requestID string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"request_id":   requestID,
		"command_type": "git.clone",
		"payload":      json.RawMessage(`{}`),
		"timeout_ms":   1000,
		"user_id":      pendingDrainUser,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = e.js.Publish(ctx, pendingSubjectPrefix+sanitizePendingSubjectToken(daemonID), data)
	require.NoError(t, err)
}

func (e *pendingClusterEnv) consumerCount(t *testing.T) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := e.stream.Info(ctx)
	require.NoError(t, err)
	return info.State.Consumers
}

func (e *pendingClusterEnv) newBridge(mgr DaemonConnectionManager) *NATSToolBridge {
	return newTestBridge(e.js, mgr)
}

func requestIDs(m *recordingDaemonMgr) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for _, c := range m.commands {
		ids = append(ids, c.RequestId)
	}
	return ids
}

// Idle polls for many connected daemons create no consumers and leave every
// member healthy. This is the prod regression: the old drain created and
// deleted an R3 consumer per daemon per poll.
func TestDrainPendingCommands_Cluster_IdlePollsDoNotChurnConsumers(t *testing.T) {
	env := newPendingClusterEnv(t)
	mgr := &recordingDaemonMgr{}
	bridge := env.newBridge(mgr)
	defer bridge.cancel()

	const daemons = 8
	for round := 0; round < 6; round++ {
		for d := 0; d < daemons; d++ {
			bridge.drainPendingCommands(context.Background(), pendingDrainUser, fmt.Sprintf("daemon-%d", d))
		}
		env.cluster.assertHealthy(t, fmt.Sprintf("idle round %d", round))
	}

	assert.Zero(t, env.consumerCount(t), "idle polls must leave no consumer behind")
	assert.Zero(t, env.consumerCreates.Load(), "idle polls must not create consumers")
	assert.Empty(t, requestIDs(mgr))
}

// A command published while the daemon's consumer already exists is delivered
// exactly once, in order, and the consumer is created once and reused.
func TestDrainPendingCommands_Cluster_ExactlyOnceInOrderWithExistingConsumer(t *testing.T) {
	env := newPendingClusterEnv(t)
	mgr := &recordingDaemonMgr{}
	bridge := env.newBridge(mgr)
	defer bridge.cancel()
	const daemon = "daemon-1"

	env.publish(t, daemon, "req-1")
	bridge.drainPendingCommands(context.Background(), pendingDrainUser, daemon)
	require.Equal(t, []string{"req-1"}, requestIDs(mgr))
	require.Eventually(t, func() bool { return env.consumerCreates.Load() == 1 },
		5*time.Second, 50*time.Millisecond, "advisory must observe the one consumer create (guards against a vacuous count)")

	// The consumer now exists; later commands must flow through it.
	env.publish(t, daemon, "req-2")
	env.publish(t, daemon, "req-3")
	bridge.drainPendingCommands(context.Background(), pendingDrainUser, daemon)
	// A further idle poll must not redeliver anything.
	bridge.drainPendingCommands(context.Background(), pendingDrainUser, daemon)

	assert.Equal(t, []string{"req-1", "req-2", "req-3"}, requestIDs(mgr), "exactly once, in order")
	assert.Equal(t, int64(1), env.consumerCreates.Load(), "the consumer is reused, not re-created")
	assert.Equal(t, 1, env.consumerCount(t))
	env.cluster.assertHealthy(t, "after drains")

	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		info, err := env.stream.Info(ctx)
		return err == nil && info.State.Msgs == 0
	}, 10*time.Second, 100*time.Millisecond, "acked commands must leave the WorkQueue stream")
}

// A durable left behind by the previous release (same name, same filter,
// created by the old per-poll CreateOrUpdate and never deleted because a drain
// was interrupted or had a Nak) is adopted, not duplicated or rejected.
func TestDrainPendingCommands_Cluster_AdoptsConsumerLeftByOldCode(t *testing.T) {
	env := newPendingClusterEnv(t)
	const daemon = "daemon-legacy"
	name := "pending-drain-" + sanitizePendingSubjectToken(daemon)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := env.stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Name:              name,
		Durable:           name,
		FilterSubject:     pendingSubjectPrefix + sanitizePendingSubjectToken(daemon),
		AckPolicy:         jetstream.AckExplicitPolicy,
		DeliverPolicy:     jetstream.DeliverAllPolicy,
		AckWait:           30 * time.Second,
		InactiveThreshold: 1 * time.Minute,
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return env.consumerCreates.Load() == 1 },
		5*time.Second, 50*time.Millisecond, "advisory must observe the legacy consumer create")
	created := env.consumerCreates.Load()

	mgr := &recordingDaemonMgr{}
	bridge := env.newBridge(mgr)
	defer bridge.cancel()

	// Idle poll first: still must not touch the leftover.
	bridge.drainPendingCommands(context.Background(), pendingDrainUser, daemon)
	env.publish(t, daemon, "req-1")
	bridge.drainPendingCommands(context.Background(), pendingDrainUser, daemon)

	assert.Equal(t, []string{"req-1"}, requestIDs(mgr))
	assert.Equal(t, created, env.consumerCreates.Load(), "leftover reused: no second create")
	assert.Equal(t, 1, env.consumerCount(t))
	env.cluster.assertHealthy(t, "after adopting legacy consumer")
}

// Pending commands survive a gateway restart: whatever the old gateway never
// drained is delivered by a fresh bridge, once.
func TestDrainPendingCommands_Cluster_SurvivesGatewayRestart(t *testing.T) {
	env := newPendingClusterEnv(t)
	const daemon = "daemon-restart"

	env.publish(t, daemon, "req-1")
	env.publish(t, daemon, "req-2")

	// "Old gateway" connects, its daemon is not drained yet, then it goes away.
	oldBridge := env.newBridge(&recordingDaemonMgr{})
	oldBridge.cancel()

	mgr := &recordingDaemonMgr{}
	newBridge := env.newBridge(mgr)
	defer newBridge.cancel()
	newBridge.drainPendingCommands(context.Background(), pendingDrainUser, daemon)

	assert.Equal(t, []string{"req-1", "req-2"}, requestIDs(mgr))

	// A second restart finds nothing left to replay.
	mgr2 := &recordingDaemonMgr{}
	b3 := env.newBridge(mgr2)
	defer b3.cancel()
	b3.drainPendingCommands(context.Background(), pendingDrainUser, daemon)
	assert.Empty(t, requestIDs(mgr2))
	env.cluster.assertHealthy(t, "after restart drains")
}

// A route port is picked before its member binds it, so on a busy machine
// another process can take it in between — an outbound connection draws its
// local port from the same ephemeral range. The member then never listens for
// routes, and with NoLog nothing says why: CI saw only "cluster member not
// ready" after the full 20s. Startup must notice and move to fresh ports.
func TestStartNATSCluster_RecoversWhenARoutePortIsTaken(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: boots a 3-node NATS cluster; runs in the full lane")
	}
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })

	var handedOut atomic.Bool
	prev := routePort
	routePort = func(t *testing.T) int {
		if handedOut.CompareAndSwap(false, true) {
			return taken.Addr().(*net.TCPAddr).Port
		}
		return prev(t)
	}
	t.Cleanup(func() { routePort = prev })

	start := time.Now()
	c := startNATSCluster(t, 3)
	require.Len(t, c.servers, 3)
	assert.Less(t, time.Since(start), 15*time.Second,
		"a lost route port must be detected when the bind fails, not after the readiness timeout")
}
