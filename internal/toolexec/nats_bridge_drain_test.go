package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Stubs for DaemonConnectionManager
// ---------------------------------------------------------------------------

type recordingDaemonMgr struct {
	mu       sync.Mutex
	commands []*reliantv1.DaemonCommandRequest
	err      error
}

func (m *recordingDaemonMgr) SendDaemonCommand(_ context.Context, _ string, req *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commands = append(m.commands, req)
	if m.err != nil {
		return nil, m.err
	}
	return &reliantv1.DaemonCommandResponse{Success: true}, nil
}

func (m *recordingDaemonMgr) IsDaemonOnline(context.Context, string) bool { return true }
func (m *recordingDaemonMgr) ListConnectedDaemons(string) []DaemonInfo    { return nil }
func (m *recordingDaemonMgr) SendToolRequest(context.Context, string, *ToolExecutionRequest) error {
	return nil
}
func (m *recordingDaemonMgr) SendToolRequestSync(context.Context, string, *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	return nil, nil
}
func (m *recordingDaemonMgr) SendToolExecutionCancel(context.Context, string, string, string) error {
	return nil
}
func (m *recordingDaemonMgr) SendKillProcess(string, string) error { return nil }
func (m *recordingDaemonMgr) SendLoadProjectConfigs(context.Context, string, string, string) error {
	return nil
}
func (m *recordingDaemonMgr) SendWatchProjectConfigs(context.Context, string, string, bool) error {
	return nil
}
func (m *recordingDaemonMgr) SendTerminalInput(string, string, []byte) error { return nil }
func (m *recordingDaemonMgr) SendTerminalResize(string, string, uint32, uint32) error {
	return nil
}
func (m *recordingDaemonMgr) SubscribeTerminalOutput(string, string) (<-chan *TerminalOutputEvent, func(), error) {
	return nil, func() {}, nil
}
func (m *recordingDaemonMgr) SubscribeProcessOutput(string, string, bool) (<-chan *ProcessOutputEvent, func(), error) {
	return nil, func() {}, nil
}

// ---------------------------------------------------------------------------
// Minimal JetStream stubs — only the methods used by drainPendingCommands
// are implemented; the rest panic.
// ---------------------------------------------------------------------------

// stubMsg implements jetstream.Msg.
type stubMsg struct {
	data []byte
	// numDelivered is what Metadata() reports (1 == first delivery). Set by
	// tests that exercise the bounded-retry guard.
	numDelivered uint64
	mu           sync.Mutex
	acked        bool
	naked        bool
}

func (m *stubMsg) Data() []byte    { return m.data }
func (m *stubMsg) Subject() string { return "" }
func (m *stubMsg) Reply() string   { return "" }
func (m *stubMsg) Headers() nats.Header {
	return nats.Header{}
}
func (m *stubMsg) Ack() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acked = true
	return nil
}
func (m *stubMsg) DoubleAck(context.Context) error { return nil }
func (m *stubMsg) Nak() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.naked = true
	return nil
}
func (m *stubMsg) NakWithDelay(time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.naked = true
	return nil
}
func (m *stubMsg) InProgress() error           { return nil }
func (m *stubMsg) Term() error                 { return nil }
func (m *stubMsg) TermWithReason(string) error { return nil }
func (m *stubMsg) Metadata() (*jetstream.MsgMetadata, error) {
	nd := m.numDelivered
	if nd == 0 {
		nd = 1 // JetStream reports 1 on the first delivery.
	}
	return &jetstream.MsgMetadata{NumDelivered: nd}, nil
}

func (m *stubMsg) wasAcked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.acked
}

func (m *stubMsg) wasNaked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.naked
}

// stubMessageBatch implements jetstream.MessageBatch.
type stubMessageBatch struct {
	msgs []jetstream.Msg
	err  error
}

func (b *stubMessageBatch) Messages() <-chan jetstream.Msg {
	ch := make(chan jetstream.Msg, len(b.msgs))
	for _, m := range b.msgs {
		ch <- m
	}
	close(ch)
	return ch
}

func (b *stubMessageBatch) Error() error { return b.err }

// stubConsumer implements jetstream.Consumer — only Fetch is used.
type stubConsumer struct {
	batches []jetstream.MessageBatch
	mu      sync.Mutex
	callIdx int
}

func (c *stubConsumer) Fetch(_ int, _ ...jetstream.FetchOpt) (jetstream.MessageBatch, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.callIdx >= len(c.batches) {
		return &stubMessageBatch{err: jetstream.ErrMsgIteratorClosed}, nil
	}
	b := c.batches[c.callIdx]
	c.callIdx++
	return b, nil
}

func (c *stubConsumer) FetchBytes(int, ...jetstream.FetchOpt) (jetstream.MessageBatch, error) {
	panic("not implemented")
}
func (c *stubConsumer) FetchNoWait(int) (jetstream.MessageBatch, error) { panic("not implemented") }
func (c *stubConsumer) Consume(jetstream.MessageHandler, ...jetstream.PullConsumeOpt) (jetstream.ConsumeContext, error) {
	panic("not implemented")
}
func (c *stubConsumer) Messages(...jetstream.PullMessagesOpt) (jetstream.MessagesContext, error) {
	panic("not implemented")
}
func (c *stubConsumer) Next(...jetstream.FetchOpt) (jetstream.Msg, error) { panic("not implemented") }
func (c *stubConsumer) Info(context.Context) (*jetstream.ConsumerInfo, error) {
	return &jetstream.ConsumerInfo{Name: "stub-consumer"}, nil
}
func (c *stubConsumer) CachedInfo() *jetstream.ConsumerInfo { panic("not implemented") }

// stubStream implements jetstream.Stream — only consumer create/delete is used.
type stubStream struct {
	consumer         jetstream.Consumer
	createErr        error
	lastConsumerCfg  jetstream.ConsumerConfig
	deletedConsumers []string
}

func (s *stubStream) OrderedConsumer(_ context.Context, _ jetstream.OrderedConsumerConfig) (jetstream.Consumer, error) {
	return s.consumer, nil
}

// createErr, when set, is returned by CreateOrUpdateConsumer to simulate a
// consumer-creation failure (e.g. WorkQueue overlap on fast reconnect).
func (s *stubStream) CreateOrUpdateConsumer(_ context.Context, cfg jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	s.lastConsumerCfg = cfg
	if s.createErr != nil {
		return nil, s.createErr
	}
	return s.consumer, nil
}
func (s *stubStream) CreateConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return s.consumer, nil
}
func (s *stubStream) UpdateConsumer(context.Context, jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	panic("not implemented")
}
func (s *stubStream) Consumer(context.Context, string) (jetstream.Consumer, error) {
	panic("not implemented")
}
func (s *stubStream) DeleteConsumer(_ context.Context, name string) error {
	s.deletedConsumers = append(s.deletedConsumers, name)
	return nil
}
func (s *stubStream) PauseConsumer(context.Context, string, time.Time) (*jetstream.ConsumerPauseResponse, error) {
	panic("not implemented")
}
func (s *stubStream) ResumeConsumer(context.Context, string) (*jetstream.ConsumerPauseResponse, error) {
	panic("not implemented")
}
func (s *stubStream) ListConsumers(context.Context) jetstream.ConsumerInfoLister {
	panic("not implemented")
}
func (s *stubStream) ConsumerNames(context.Context) jetstream.ConsumerNameLister {
	panic("not implemented")
}
func (s *stubStream) UnpinConsumer(context.Context, string, string) error { panic("not implemented") }
func (s *stubStream) CreateOrUpdatePushConsumer(context.Context, jetstream.ConsumerConfig) (jetstream.PushConsumer, error) {
	panic("not implemented")
}
func (s *stubStream) CreatePushConsumer(context.Context, jetstream.ConsumerConfig) (jetstream.PushConsumer, error) {
	panic("not implemented")
}
func (s *stubStream) UpdatePushConsumer(context.Context, jetstream.ConsumerConfig) (jetstream.PushConsumer, error) {
	panic("not implemented")
}
func (s *stubStream) PushConsumer(context.Context, string) (jetstream.PushConsumer, error) {
	panic("not implemented")
}
func (s *stubStream) Info(context.Context, ...jetstream.StreamInfoOpt) (*jetstream.StreamInfo, error) {
	panic("not implemented")
}
func (s *stubStream) CachedInfo() *jetstream.StreamInfo { panic("not implemented") }
func (s *stubStream) Purge(context.Context, ...jetstream.StreamPurgeOpt) error {
	panic("not implemented")
}
func (s *stubStream) GetMsg(context.Context, uint64, ...jetstream.GetMsgOpt) (*jetstream.RawStreamMsg, error) {
	panic("not implemented")
}
func (s *stubStream) GetLastMsgForSubject(context.Context, string) (*jetstream.RawStreamMsg, error) {
	panic("not implemented")
}
func (s *stubStream) DeleteMsg(context.Context, uint64) error       { panic("not implemented") }
func (s *stubStream) SecureDeleteMsg(context.Context, uint64) error { panic("not implemented") }

// stubJetStream implements jetstream.JetStream — only Stream is used.
type stubJetStream struct {
	stream    jetstream.Stream
	streamErr error
}

func (js *stubJetStream) Stream(_ context.Context, _ string) (jetstream.Stream, error) {
	if js.streamErr != nil {
		return nil, js.streamErr
	}
	return js.stream, nil
}

func (js *stubJetStream) AccountInfo(context.Context) (*jetstream.AccountInfo, error) {
	panic("not implemented")
}
func (js *stubJetStream) Conn() *nats.Conn                    { panic("not implemented") }
func (js *stubJetStream) Options() jetstream.JetStreamOptions { panic("not implemented") }
func (js *stubJetStream) CreateStream(context.Context, jetstream.StreamConfig) (jetstream.Stream, error) {
	panic("not implemented")
}
func (js *stubJetStream) UpdateStream(context.Context, jetstream.StreamConfig) (jetstream.Stream, error) {
	panic("not implemented")
}
func (js *stubJetStream) CreateOrUpdateStream(context.Context, jetstream.StreamConfig) (jetstream.Stream, error) {
	panic("not implemented")
}
func (js *stubJetStream) DeleteStream(context.Context, string) error { panic("not implemented") }
func (js *stubJetStream) ListStreams(context.Context, ...jetstream.StreamListOpt) jetstream.StreamInfoLister {
	panic("not implemented")
}
func (js *stubJetStream) StreamNames(context.Context, ...jetstream.StreamListOpt) jetstream.StreamNameLister {
	panic("not implemented")
}
func (js *stubJetStream) StreamNameBySubject(context.Context, string) (string, error) {
	panic("not implemented")
}
func (js *stubJetStream) CreateOrUpdateConsumer(context.Context, string, jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	panic("not implemented")
}
func (js *stubJetStream) CreateConsumer(context.Context, string, jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	panic("not implemented")
}
func (js *stubJetStream) UpdateConsumer(context.Context, string, jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	panic("not implemented")
}
func (js *stubJetStream) OrderedConsumer(context.Context, string, jetstream.OrderedConsumerConfig) (jetstream.Consumer, error) {
	panic("not implemented")
}
func (js *stubJetStream) Consumer(context.Context, string, string) (jetstream.Consumer, error) {
	panic("not implemented")
}
func (js *stubJetStream) DeleteConsumer(context.Context, string, string) error {
	panic("not implemented")
}
func (js *stubJetStream) PauseConsumer(context.Context, string, string, time.Time) (*jetstream.ConsumerPauseResponse, error) {
	panic("not implemented")
}
func (js *stubJetStream) ResumeConsumer(context.Context, string, string) (*jetstream.ConsumerPauseResponse, error) {
	panic("not implemented")
}
func (js *stubJetStream) CreateOrUpdatePushConsumer(context.Context, string, jetstream.ConsumerConfig) (jetstream.PushConsumer, error) {
	panic("not implemented")
}
func (js *stubJetStream) CreatePushConsumer(context.Context, string, jetstream.ConsumerConfig) (jetstream.PushConsumer, error) {
	panic("not implemented")
}
func (js *stubJetStream) UpdatePushConsumer(context.Context, string, jetstream.ConsumerConfig) (jetstream.PushConsumer, error) {
	panic("not implemented")
}
func (js *stubJetStream) PushConsumer(context.Context, string, string) (jetstream.PushConsumer, error) {
	panic("not implemented")
}
func (js *stubJetStream) Publish(context.Context, string, []byte, ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	panic("not implemented")
}
func (js *stubJetStream) PublishMsg(context.Context, *nats.Msg, ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	panic("not implemented")
}
func (js *stubJetStream) PublishAsync(string, []byte, ...jetstream.PublishOpt) (jetstream.PubAckFuture, error) {
	panic("not implemented")
}
func (js *stubJetStream) PublishMsgAsync(*nats.Msg, ...jetstream.PublishOpt) (jetstream.PubAckFuture, error) {
	panic("not implemented")
}
func (js *stubJetStream) PublishAsyncPending() int              { return 0 }
func (js *stubJetStream) PublishAsyncComplete() <-chan struct{} { return nil }
func (js *stubJetStream) CleanupPublisher()                     {}
func (js *stubJetStream) KeyValue(context.Context, string) (jetstream.KeyValue, error) {
	panic("not implemented")
}
func (js *stubJetStream) CreateKeyValue(context.Context, jetstream.KeyValueConfig) (jetstream.KeyValue, error) {
	panic("not implemented")
}
func (js *stubJetStream) UpdateKeyValue(context.Context, jetstream.KeyValueConfig) (jetstream.KeyValue, error) {
	panic("not implemented")
}
func (js *stubJetStream) CreateOrUpdateKeyValue(context.Context, jetstream.KeyValueConfig) (jetstream.KeyValue, error) {
	panic("not implemented")
}
func (js *stubJetStream) DeleteKeyValue(context.Context, string) error { panic("not implemented") }
func (js *stubJetStream) KeyValueStoreNames(context.Context) jetstream.KeyValueNamesLister {
	panic("not implemented")
}
func (js *stubJetStream) KeyValueStores(context.Context) jetstream.KeyValueLister {
	panic("not implemented")
}
func (js *stubJetStream) ObjectStore(context.Context, string) (jetstream.ObjectStore, error) {
	panic("not implemented")
}
func (js *stubJetStream) CreateObjectStore(context.Context, jetstream.ObjectStoreConfig) (jetstream.ObjectStore, error) {
	panic("not implemented")
}
func (js *stubJetStream) UpdateObjectStore(context.Context, jetstream.ObjectStoreConfig) (jetstream.ObjectStore, error) {
	panic("not implemented")
}
func (js *stubJetStream) CreateOrUpdateObjectStore(context.Context, jetstream.ObjectStoreConfig) (jetstream.ObjectStore, error) {
	panic("not implemented")
}
func (js *stubJetStream) DeleteObjectStore(context.Context, string) error { panic("not implemented") }
func (js *stubJetStream) ObjectStoreNames(context.Context) jetstream.ObjectStoreNamesLister {
	panic("not implemented")
}
func (js *stubJetStream) ObjectStores(context.Context) jetstream.ObjectStoresLister {
	panic("not implemented")
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func newTestBridge(js jetstream.JetStream, mgr DaemonConnectionManager) *NATSToolBridge {
	ctx, cancel := context.WithCancel(context.Background())
	return &NATSToolBridge{
		nc:            nil,
		js:            js,
		mgr:           mgr,
		daemonSubs:    make(map[string][]*nats.Subscription),
		daemonCancels: make(map[string]context.CancelFunc),
		ctx:           ctx,
		cancel:        cancel,
	}
}

func makePendingMsg(t *testing.T, requestID, commandType string, payload json.RawMessage, timeoutMs int32) *stubMsg {
	t.Helper()
	data, err := json.Marshal(struct {
		RequestID   string          `json:"request_id"`
		CommandType string          `json:"command_type"`
		Payload     json.RawMessage `json:"payload"`
		TimeoutMs   int32           `json:"timeout_ms"`
	}{requestID, commandType, payload, timeoutMs})
	require.NoError(t, err)
	return &stubMsg{data: data}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDrainPendingCommands_NilJetStream(t *testing.T) {
	mgr := &recordingDaemonMgr{}
	bridge := newTestBridge(nil, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Empty(t, mgr.commands)
}

func TestDrainPendingCommands_StreamNotFound(t *testing.T) {
	mgr := &recordingDaemonMgr{}
	js := &stubJetStream{streamErr: jetstream.ErrStreamNotFound}
	bridge := newTestBridge(js, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Empty(t, mgr.commands)
}

func TestDrainPendingCommands_StreamLookupError(t *testing.T) {
	mgr := &recordingDaemonMgr{}
	js := &stubJetStream{streamErr: errors.New("connection refused")}
	bridge := newTestBridge(js, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Empty(t, mgr.commands)
}

func TestDrainPendingCommands_NoPendingMessages(t *testing.T) {
	mgr := &recordingDaemonMgr{}
	consumer := &stubConsumer{
		batches: []jetstream.MessageBatch{
			&stubMessageBatch{err: jetstream.ErrMsgIteratorClosed},
		},
	}
	stream := &stubStream{consumer: consumer}
	js := &stubJetStream{stream: stream}
	bridge := newTestBridge(js, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Empty(t, mgr.commands)
}

func TestDrainPendingCommands_DispatchesPendingMessages(t *testing.T) {
	mgr := &recordingDaemonMgr{}

	msg1 := makePendingMsg(t, "req-1", "git.clone", json.RawMessage(`{"url":"https://example.com/repo"}`), 30000)
	msg2 := makePendingMsg(t, "req-2", "git.pull", json.RawMessage(`{"branch":"main"}`), 15000)

	consumer := &stubConsumer{
		batches: []jetstream.MessageBatch{
			&stubMessageBatch{
				msgs: []jetstream.Msg{msg1, msg2},
				err:  jetstream.ErrMsgIteratorClosed,
			},
		},
	}
	stream := &stubStream{consumer: consumer}
	js := &stubJetStream{stream: stream}
	bridge := newTestBridge(js, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	require.Len(t, mgr.commands, 2)

	assert.Equal(t, "req-1", mgr.commands[0].RequestId)
	assert.Equal(t, "git.clone", mgr.commands[0].CommandType)
	assert.Equal(t, int32(30000), mgr.commands[0].TimeoutMs)

	assert.Equal(t, "req-2", mgr.commands[1].RequestId)
	assert.Equal(t, "git.pull", mgr.commands[1].CommandType)
	assert.Equal(t, int32(15000), mgr.commands[1].TimeoutMs)
}

func TestDrainPendingCommands_MessagesAreAcked(t *testing.T) {
	mgr := &recordingDaemonMgr{}

	msg1 := makePendingMsg(t, "req-1", "git.clone", json.RawMessage(`{}`), 5000)
	msg2 := makePendingMsg(t, "req-2", "git.pull", json.RawMessage(`{}`), 5000)

	consumer := &stubConsumer{
		batches: []jetstream.MessageBatch{
			&stubMessageBatch{
				msgs: []jetstream.Msg{msg1, msg2},
				err:  jetstream.ErrMsgIteratorClosed,
			},
		},
	}
	stream := &stubStream{consumer: consumer}
	js := &stubJetStream{stream: stream}
	bridge := newTestBridge(js, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	assert.True(t, msg1.wasAcked(), "msg1 should be acked after dispatch")
	assert.True(t, msg2.wasAcked(), "msg2 should be acked after dispatch")
}

// A transient dispatch failure must NOT ack (which would permanently drop the
// command under WorkQueue retention) — it Naks for JetStream redelivery.
func TestDrainPendingCommands_DispatchErrorNaksNotAcks(t *testing.T) {
	mgr := &recordingDaemonMgr{err: errors.New("daemon busy")}

	msg1 := makePendingMsg(t, "req-1", "git.clone", json.RawMessage(`{}`), 5000)

	consumer := &stubConsumer{
		batches: []jetstream.MessageBatch{
			&stubMessageBatch{
				msgs: []jetstream.Msg{msg1},
				err:  jetstream.ErrMsgIteratorClosed,
			},
		},
	}
	stream := &stubStream{consumer: consumer}
	js := &stubJetStream{stream: stream}
	bridge := newTestBridge(js, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	assert.False(t, msg1.wasAcked(), "msg must NOT be acked on transient dispatch error")
	assert.True(t, msg1.wasNaked(), "msg must be Nak'd for redelivery on dispatch error")
	// The consumer must survive so NumDelivered accounting persists.
	assert.Empty(t, stream.deletedConsumers,
		"consumer must not be deleted while messages are pending redelivery")
}

// After maxPendingCommandDeliveries the command is a poison message: ack it
// (drop) rather than loop forever.
func TestDrainPendingCommands_PoisonMessageDroppedAfterMaxDeliveries(t *testing.T) {
	mgr := &recordingDaemonMgr{err: errors.New("daemon busy")}

	msg1 := makePendingMsg(t, "req-1", "git.clone", json.RawMessage(`{}`), 5000)
	msg1.numDelivered = maxPendingCommandDeliveries // already at the cap

	consumer := &stubConsumer{
		batches: []jetstream.MessageBatch{
			&stubMessageBatch{
				msgs: []jetstream.Msg{msg1},
				err:  jetstream.ErrMsgIteratorClosed,
			},
		},
	}
	stream := &stubStream{consumer: consumer}
	js := &stubJetStream{stream: stream}
	bridge := newTestBridge(js, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	assert.True(t, msg1.wasAcked(), "poison msg should be acked (dropped) after max deliveries")
	assert.False(t, msg1.wasNaked(), "poison msg should not be Nak'd again")
}

// The drain retries a previously-failed command on the next connect and
// succeeds — the end-to-end redelivery path.
func TestDrainPendingCommands_SucceedsOnRetryAfterTransientFailure(t *testing.T) {
	// First drain: dispatch fails -> Nak, not acked.
	failMgr := &recordingDaemonMgr{err: errors.New("daemon busy")}
	msg := makePendingMsg(t, "req-1", "git.clone", json.RawMessage(`{}`), 5000)

	firstConsumer := &stubConsumer{
		batches: []jetstream.MessageBatch{
			&stubMessageBatch{msgs: []jetstream.Msg{msg}, err: jetstream.ErrMsgIteratorClosed},
		},
	}
	stream1 := &stubStream{consumer: firstConsumer}
	bridge1 := newTestBridge(&stubJetStream{stream: stream1}, failMgr)
	defer bridge1.cancel()
	bridge1.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	require.False(t, msg.wasAcked(), "first attempt must not ack")
	require.True(t, msg.wasNaked(), "first attempt must Nak")

	// Simulate JetStream redelivery on the next connect: same message, now
	// delivered a second time, daemon healthy.
	msg.mu.Lock()
	msg.naked = false
	msg.numDelivered = 2
	msg.mu.Unlock()

	okMgr := &recordingDaemonMgr{}
	secondConsumer := &stubConsumer{
		batches: []jetstream.MessageBatch{
			&stubMessageBatch{msgs: []jetstream.Msg{msg}, err: jetstream.ErrMsgIteratorClosed},
		},
	}
	stream2 := &stubStream{consumer: secondConsumer}
	bridge2 := newTestBridge(&stubJetStream{stream: stream2}, okMgr)
	defer bridge2.cancel()
	bridge2.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	assert.True(t, msg.wasAcked(), "retry must ack on success")
	assert.False(t, msg.wasNaked(), "retry must not Nak on success")
	okMgr.mu.Lock()
	defer okMgr.mu.Unlock()
	require.Len(t, okMgr.commands, 1)
	assert.Equal(t, "git.clone", okMgr.commands[0].CommandType)
}

// A daemonID containing characters SanitizeSubject rewrites must still be
// drained end-to-end: the consumer FilterSubject must match the sanitized
// subject the enqueue side publishes to.
func TestDrainPendingCommands_SanitizesDaemonIDInFilterSubject(t *testing.T) {
	mgr := &recordingDaemonMgr{}
	msg := makePendingMsg(t, "req-1", "git.clone", json.RawMessage(`{}`), 5000)

	consumer := &stubConsumer{
		batches: []jetstream.MessageBatch{
			&stubMessageBatch{msgs: []jetstream.Msg{msg}, err: jetstream.ErrMsgIteratorClosed},
		},
	}
	stream := &stubStream{consumer: consumer}
	js := &stubJetStream{stream: stream}
	bridge := newTestBridge(js, mgr)
	defer bridge.cancel()

	// daemonID with a '.' and a '*' — both rewritten to '_' by the sanitizer.
	const rawID = "team.alpha*1 x"
	const wantToken = "team_alpha_1_x"

	bridge.drainPendingCommands(context.Background(), "user-1", rawID)

	// The consumer must filter on the SANITIZED subject (matching what
	// EnqueueDaemonCommand / control-plane publish to), else the message
	// would never be delivered.
	assert.Equal(t, pendingSubjectPrefix+wantToken, stream.lastConsumerCfg.FilterSubject,
		"FilterSubject must use the sanitized daemonID")
	assert.Equal(t, sanitizePendingSubjectToken(rawID), wantToken,
		"sanitizer must rewrite '.', '*' and ' ' to '_'")

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Len(t, mgr.commands, 1, "command must be dispatched despite special chars in daemonID")
	assert.True(t, msg.wasAcked())
}

// On successful clean drain the named consumer is deleted (best-effort cleanup).
func TestDrainPendingCommands_DeletesConsumerOnCleanDrain(t *testing.T) {
	mgr := &recordingDaemonMgr{}
	msg := makePendingMsg(t, "req-1", "git.clone", json.RawMessage(`{}`), 5000)

	consumer := &stubConsumer{
		batches: []jetstream.MessageBatch{
			&stubMessageBatch{msgs: []jetstream.Msg{msg}, err: jetstream.ErrMsgIteratorClosed},
		},
	}
	stream := &stubStream{consumer: consumer}
	js := &stubJetStream{stream: stream}
	bridge := newTestBridge(js, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	require.Len(t, stream.deletedConsumers, 1, "clean drain should delete its consumer")
	assert.Equal(t, "pending-drain-daemon-1", stream.deletedConsumers[0])
}

// When CreateOrUpdateConsumer fails, the drain gives up without dispatching.
func TestDrainPendingCommands_ConsumerCreateError(t *testing.T) {
	mgr := &recordingDaemonMgr{}
	stream := &stubStream{createErr: errors.New("overlapping filter subject")}
	js := &stubJetStream{stream: stream}
	bridge := newTestBridge(js, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Empty(t, mgr.commands)
}

func TestDrainPendingCommands_InvalidPayloadAcksAndContinues(t *testing.T) {
	mgr := &recordingDaemonMgr{}

	badMsg := &stubMsg{data: []byte("not json")}
	goodMsg := makePendingMsg(t, "req-2", "git.pull", json.RawMessage(`{}`), 5000)

	consumer := &stubConsumer{
		batches: []jetstream.MessageBatch{
			&stubMessageBatch{
				msgs: []jetstream.Msg{badMsg, goodMsg},
				err:  jetstream.ErrMsgIteratorClosed,
			},
		},
	}
	stream := &stubStream{consumer: consumer}
	js := &stubJetStream{stream: stream}
	bridge := newTestBridge(js, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	assert.True(t, badMsg.wasAcked(), "invalid msg should be acked")
	assert.True(t, goodMsg.wasAcked(), "valid msg should be acked")

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Len(t, mgr.commands, 1)
	assert.Equal(t, "req-2", mgr.commands[0].RequestId)
}

func TestDrainPendingCommands_MultipleFetchBatches(t *testing.T) {
	mgr := &recordingDaemonMgr{}

	msg1 := makePendingMsg(t, "req-1", "cmd-1", json.RawMessage(`{}`), 1000)
	msg2 := makePendingMsg(t, "req-2", "cmd-2", json.RawMessage(`{}`), 2000)

	consumer := &stubConsumer{
		batches: []jetstream.MessageBatch{
			&stubMessageBatch{msgs: []jetstream.Msg{msg1}},
			&stubMessageBatch{
				msgs: []jetstream.Msg{msg2},
				err:  jetstream.ErrMsgIteratorClosed,
			},
		},
	}
	stream := &stubStream{consumer: consumer}
	js := &stubJetStream{stream: stream}
	bridge := newTestBridge(js, mgr)
	defer bridge.cancel()

	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Len(t, mgr.commands, 2)
	assert.Equal(t, "req-1", mgr.commands[0].RequestId)
	assert.Equal(t, "req-2", mgr.commands[1].RequestId)
}
