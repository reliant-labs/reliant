// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
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

	"github.com/reliant-labs/reliant/internal/memtemp"
)

// These tests run drainPendingCommands against a real 3-node embedded NATS
// cluster holding DAEMON_PENDING_COMMANDS as an R3 WorkQueue stream — the prod
// shape. The prod failure was invisible to the stub-based drain tests: every
// consumer on an R3 stream is its own raft group, and creating/deleting one
// per poll per daemon kept followers "not current", failing /healthz.

type natsTestCluster struct {
	servers []*server.Server
	// routesPerPeer is how many route connections a member holds to each
	// peer once the mesh is complete: one per pool slot plus one per pinned
	// account. NewServer fills both defaults into the options it is given.
	routesPerPeer int
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

// clusterAttempts is how many fresh clusters startNATSCluster boots before
// giving up. A fresh attempt gets fresh route ports and fresh store dirs.
const clusterAttempts = 3

// maxFormationWait caps one attempt's wait for the cluster to form. On an
// idle machine it forms in under a second; the cap exists only so a wedged
// attempt is abandoned and retried while the test binary still has time left.
const maxFormationWait = 90 * time.Second

// errClusterStalled: the members started but the cluster did not finish
// forming within the attempt's budget. Retried on a fresh cluster.
var errClusterStalled = errors.New("cluster did not finish forming")

func startNATSCluster(t *testing.T, n int) *natsTestCluster {
	t.Helper()
	// A taken route port and a stalled formation are retried on a fresh
	// cluster: both are the machine being busy, not the cluster misbehaving.
	// Any other fatal startup error (an unwritable store dir, a bad option)
	// would fail every attempt identically, so it fails at once.
	for attempt := 1; ; attempt++ {
		budget := attemptBudget(t, clusterAttempts-attempt+1)
		c, err := bootNATSCluster(t, n, budget)
		if err == nil {
			return c
		}
		retryable := errors.Is(err, errRoutePortTaken) || errors.Is(err, errClusterStalled)
		if !retryable || attempt == clusterAttempts {
			t.Fatalf("NATS cluster did not start (attempt %d of %d): %v", attempt, clusterAttempts, err)
		}
		t.Logf("NATS cluster attempt %d of %d: %v; retrying on a fresh cluster", attempt, clusterAttempts, err)
	}
}

// formationBudget is how long one attempt may wait for the cluster to form:
// an even share of what the test binary's -timeout leaves, minus room for the
// test body and cleanup, capped at maxFormationWait. Waiting on the cluster's
// own signals for as long as the binary can afford, rather than a fixed few
// seconds, is what keeps a starved machine from failing a healthy cluster.
func formationBudget(t *testing.T, attemptsLeft int) time.Duration {
	t.Helper()
	budget := maxFormationWait
	if deadline, ok := t.Deadline(); ok {
		const reserve = 30 * time.Second
		if share := (time.Until(deadline) - reserve) / time.Duration(attemptsLeft); share < budget {
			budget = share
		}
	}
	return max(budget, 10*time.Second)
}

// storeRoot is where one attempt's members keep their JetStream state: a
// tmpfs when the machine has one with room (internal/memtemp), else the
// test's temp dir.
//
// Startup, and every raft term, vote and peer change after it, writes with
// O_SYNC, and a member listens for clients and routes only once its JetStream
// layer is up. On a disk shared with other builds that is the whole cost of
// forming the cluster: 64 concurrent O_DIRECT|O_SYNC writers on a shared
// dev machine's disk took member readiness from ~60ms to ~4s and formation from
// <1s to 22-51s, and a busier disk is how "cluster member n0 not ready after
// 20s" happened. Durability across a power cut is not what these tests are
// about, so the state lives in memory, where an fsync costs nothing.
func storeRoot(t *testing.T) string {
	t.Helper()
	return memtemp.Dir(t)
}

// attemptBudget picks each attempt's budget. A variable so a test can make an
// attempt stall.
var attemptBudget = formationBudget

// bootNATSCluster starts n members routed to each other and waits, for at
// most budget, until the cluster has formed (see formation). A member that
// reports a fatal error fails it at once rather than at the budget, and every
// member it started is shut down whenever it fails.
func bootNATSCluster(t *testing.T, n int, budget time.Duration) (*natsTestCluster, error) {
	t.Helper()
	routePorts := make([]int, n)
	for i := range routePorts {
		routePorts[i] = routePort(t)
	}
	// Before the shutdown cleanup is registered, so it is removed after
	// every member has stopped writing to it.
	root := storeRoot(t)
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
			StoreDir:   filepath.Join(root, fmt.Sprintf("n%d", i)),
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
		c.routesPerPeer = max(opts.Cluster.PoolSize, 1) + len(opts.Cluster.PinnedAccounts)
		go srv.Start()
		c.servers = append(c.servers, srv)
	}

	start := time.Now()
	nextReport := start.Add(10 * time.Second)
	for {
		for i, rec := range recorders {
			msg := rec.fatal()
			if msg == "" {
				continue
			}
			shutdown()
			if strings.Contains(msg, "Error listening on router port") {
				return nil, fmt.Errorf("%w: %s: %s", errRoutePortTaken, c.servers[i].Name(), msg)
			}
			return nil, fmt.Errorf("%s: %s", c.servers[i].Name(), msg)
		}
		formed, state := c.formation()
		if formed {
			return c, nil
		}
		now := time.Now()
		if now.Sub(start) > budget {
			shutdown()
			return nil, fmt.Errorf("%w after %s: %s", errClusterStalled, budget, state)
		}
		if now.After(nextReport) {
			t.Logf("NATS cluster still forming after %s: %s", now.Sub(start).Round(time.Second), state)
			nextReport = now.Add(10 * time.Second)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// formation reports whether the cluster has formed, and each member's state
// for the failure message when it has not. Formed means, on every member:
// listening for clients and routes, the COMPLETE route mesh, and a current
// JetStream meta layer with a leader somewhere.
//
// The complete route mesh, not just a JetStream meta leader: the members
// start together, so some of their first route dials are refused and the
// races between explicit and implicit routes end in duplicate routes being
// closed. That can leave one pool slot between two members empty until the
// 1s reconnect fills it. JetStream rides the system account's pinned route,
// so the cluster elects leaders and reports healthy, but the $G interest that
// slot carries does not cross: a stream leader's subscription never reaches
// the member the test publishes through ("nats: no response from stream"),
// and an advisory the test subscribed to is dropped by a member that never
// heard of the subscription. Captured in a protocol trace: n0's RS+ for the
// advisory reached n2 only 0.7s later, over the reconnected route, after n2
// had already published the consumer-created advisory to nobody.
// nats-server's own tests wait for exactly this (checkClusterFormed).
func (c *natsTestCluster) formation() (bool, string) {
	wantRoutes := (len(c.servers) - 1) * c.routesPerPeer
	formed, leader := true, false
	states := make([]string, 0, len(c.servers))
	for _, s := range c.servers {
		listening := s.Addr() != nil && s.ClusterAddr() != nil
		routes := s.NumRoutes()
		current, isLeader := s.JetStreamIsCurrent(), s.JetStreamIsLeader()
		leader = leader || isLeader
		formed = formed && listening && routes == wantRoutes && current
		states = append(states, fmt.Sprintf("%s{listening=%t routes=%d/%d js-current=%t js-leader=%t}",
			s.Name(), listening, routes, wantRoutes, current, isLeader))
	}
	return formed && leader, strings.Join(states, " ")
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
	}, formationBudget(t, 1), 20*time.Millisecond, "interest in %s never reached every member", subject)
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
	}, formationBudget(t, 1), 200*time.Millisecond, "cluster never became healthy")
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
	}, formationBudget(t, 1), 250*time.Millisecond, "create R3 stream: %v", err)
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

// The drain that creates a daemon's consumer delivers what is queued, every
// time. Pulling right after the create can reach a member that has not yet
// heard of the new leader's pull subscription (fetchFoundNoConsumerLeader);
// one create per test made that a 1-in-70 flake, so this makes many. The
// drains run at once, as they do when a gateway restarts with every daemon
// reconnecting: each one ends only after an empty pull's FetchMaxWait, and
// one after another they would outlast the first consumer's
// InactiveThreshold.
func TestDrainPendingCommands_Cluster_DrainThatCreatesTheConsumerDelivers(t *testing.T) {
	env := newPendingClusterEnv(t)
	mgr := &recordingDaemonMgr{}
	bridge := env.newBridge(mgr)
	defer bridge.cancel()

	const daemons = 16
	var want []string
	for d := 0; d < daemons; d++ {
		req := fmt.Sprintf("req-%d", d)
		env.publish(t, fmt.Sprintf("daemon-fresh-%d", d), req)
		want = append(want, req)
	}
	var wg sync.WaitGroup
	for d := 0; d < daemons; d++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bridge.drainPendingCommands(context.Background(), pendingDrainUser, fmt.Sprintf("daemon-fresh-%d", d))
		}()
	}
	wg.Wait()

	assert.ElementsMatch(t, want, requestIDs(mgr), "the drain that created each daemon's consumer delivers its command")
	assert.Equal(t, daemons, env.consumerCount(t), "one consumer per daemon")
	env.cluster.assertHealthy(t, "after creating a consumer per daemon")
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

// A member that is slow to start — the reported failure was "cluster member n0
// not ready after 20s" on a machine whose heavy slots were all full — is not a
// broken cluster. An attempt that runs out of budget must be abandoned and
// retried on a fresh cluster, not fail the test on attempt 1 of 3.
func TestStartNATSCluster_RetriesAStalledFormation(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: boots a 3-node NATS cluster; runs in the full lane")
	}
	var attempts atomic.Int32
	prev := attemptBudget
	attemptBudget = func(t *testing.T, attemptsLeft int) time.Duration {
		if attempts.Add(1) == 1 {
			// No cluster forms in a microsecond: this attempt stalls.
			return time.Microsecond
		}
		return prev(t, attemptsLeft)
	}
	t.Cleanup(func() { attemptBudget = prev })

	c := startNATSCluster(t, 3)
	require.Len(t, c.servers, 3)
	assert.Equal(t, int32(2), attempts.Load(), "the stalled attempt is retried once, on a fresh cluster")
	formed, state := c.formation()
	assert.True(t, formed, "the retried cluster is fully formed: %s", state)
}
