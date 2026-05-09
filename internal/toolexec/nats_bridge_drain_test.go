package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Stubs for DaemonConnectionManager (only the methods drainPendingCommands uses)
// ---------------------------------------------------------------------------

type recordingDaemonMgr struct {
	mu       sync.Mutex
	commands []*reliantv1.DaemonCommandRequest
	err      error // error to return from SendDaemonCommand
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

// Unused methods — satisfy DaemonConnectionManager interface.
func (m *recordingDaemonMgr) IsDaemonOnline(_ context.Context, _ string) bool { return true }
func (m *recordingDaemonMgr) ListConnectedDaemons(_ string) []DaemonInfo      { return nil }
func (m *recordingDaemonMgr) SendToolRequest(_ context.Context, _ string, _ *ToolExecutionRequest) error {
	return nil
}
func (m *recordingDaemonMgr) SendToolRequestSync(_ context.Context, _ string, _ *ToolExecutionRequest) (*ToolExecutionResponse, error) {
	return nil, nil
}
func (m *recordingDaemonMgr) SendToolExecutionCancel(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *recordingDaemonMgr) SendKillProcess(_, _ string) error { return nil }
func (m *recordingDaemonMgr) SendLoadProjectConfigs(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *recordingDaemonMgr) SendWatchProjectConfigs(_ context.Context, _, _ string, _ bool) error {
	return nil
}
func (m *recordingDaemonMgr) SendTerminalInput(_, _ string, _ []byte) error { return nil }
func (m *recordingDaemonMgr) SendTerminalResize(_, _ string, _, _ uint32) error {
	return nil
}
func (m *recordingDaemonMgr) SubscribeTerminalOutput(_, _ string) (<-chan *TerminalOutputEvent, func(), error) {
	return nil, func() {}, nil
}
func (m *recordingDaemonMgr) SubscribeProcessOutput(_, _ string, _ bool) (<-chan *ProcessOutputEvent, func(), error) {
	return nil, func() {}, nil
}

// ---------------------------------------------------------------------------
// Minimal JetStream stubs
// ---------------------------------------------------------------------------

// stubMsg implements jetstream.Msg for testing.
type stubMsg struct {
	data   []byte
	acked  bool
	mu     sync.Mutex
	header map[string][]string
}

func (m *stubMsg) Data() []byte    { return m.data }
func (m *stubMsg) Subject() string { return "" }
func (m *stubMsg) Reply() string   { return "" }
func (m *stubMsg) Headers() jetstream.MsgMetadata {
	return jetstream.MsgMetadata{}
}

func (m *stubMsg) Ack() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acked = true
	return nil
}
func (m *stubMsg) DoubleAck(_ context.Context) error { return nil }
func (m *stubMsg) Nak() error                        { return nil }
func (m *stubMsg) NakWithDelay(_ interface{}) error  { return nil }
func (m *stubMsg) InProgress() error                 { return nil }
func (m *stubMsg) Term() error                       { return nil }
func (m *stubMsg) TermWithReason(_ string) error     { return nil }
func (m *stubMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{}, nil
}

func (m *stubMsg) wasAcked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.acked
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
	callIdx int
	mu      sync.Mutex
}

func (c *stubConsumer) Fetch(batch int, opts ...jetstream.FetchOpt) (jetstream.MessageBatch, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.callIdx >= len(c.batches) {
		// No more batches — return empty batch with iterator-closed error.
		return &stubMessageBatch{err: jetstream.ErrMsgIteratorClosed}, nil
	}
	b := c.batches[c.callIdx]
	c.callIdx++
	return b, nil
}

func (c *stubConsumer) FetchBytes(maxBytes int, opts ...jetstream.FetchOpt) (jetstream.MessageBatch, error) {
	return nil, errors.New("not implemented")
}
func (c *stubConsumer) FetchNoWait(batch int) (jetstream.MessageBatch, error) {
	return nil, errors.New("not implemented")
}
func (c *stubConsumer) Consume(handler jetstream.MessageHandler, opts ...jetstream.PullConsumeOpt) (jetstream.ConsumeContext, error) {
	return nil, errors.New("not implemented")
}
func (c *stubConsumer) Messages(opts ...jetstream.PullMessagesOpt) (jetstream.MessagesContext, error) {
	return nil, errors.New("not implemented")
}
func (c *stubConsumer) Next(opts ...jetstream.FetchOpt) (jetstream.Msg, error) {
	return nil, errors.New("not implemented")
}
func (c *stubConsumer) Info(ctx context.Context) (*jetstream.ConsumerInfo, error) {
	return nil, errors.New("not implemented")
}
func (c *stubConsumer) CachedInfo() *jetstream.ConsumerInfo { return nil }

// stubStream implements jetstream.Stream — only OrderedConsumer is used.
type stubStream struct {
	consumer jetstream.Consumer
}

func (s *stubStream) OrderedConsumer(_ context.Context, _ jetstream.OrderedConsumerConfig) (jetstream.Consumer, error) {
	return s.consumer, nil
}

// Unused Stream methods.
func (s *stubStream) CreateOrUpdateConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return nil, errors.New("not implemented")
}
func (s *stubStream) CreateConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return nil, errors.New("not implemented")
}
func (s *stubStream) UpdateConsumer(_ context.Context, _ jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	return nil, errors.New("not implemented")
}
func (s *stubStream) Consumer(_ context.Context, _ string) (jetstream.Consumer, error) {
	return nil, errors.New("not implemented")
}
func (s *stubStream) DeleteConsumer(_ context.Context, _ string) error {
	return errors.New("not implemented")
}
func (s *stubStream) Info(_ context.Context, _ ...jetstream.StreamInfoOpt) (*jetstream.StreamInfo, error) {
	return nil, errors.New("not implemented")
}
func (s *stubStream) CachedInfo() *jetstream.StreamInfo { return nil }
func (s *stubStream) Purge(_ context.Context, _ ...jetstream.StreamPurgeOpt) error {
	return errors.New("not implemented")
}
func (s *stubStream) GetMsg(_ context.Context, _ uint64) (*jetstream.RawStreamMsg, error) {
	return nil, errors.New("not implemented")
}
func (s *stubStream) GetLastMsgForSubject(_ context.Context, _ string) (*jetstream.RawStreamMsg, error) {
	return nil, errors.New("not implemented")
}
func (s *stubStream) DeleteMsg(_ context.Context, _ uint64) error {
	return errors.New("not implemented")
}
func (s *stubStream) SecureDeleteMsg(_ context.Context, _ uint64) error {
	return errors.New("not implemented")
}
func (s *stubStream) ListConsumers(_ context.Context) jetstream.ConsumerInfoLister { return nil }
func (s *stubStream) ConsumerNames(_ context.Context) jetstream.ConsumerNameLister { return nil }

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

// Unused JetStream methods.
func (js *stubJetStream) CreateStream(_ context.Context, _ jetstream.StreamConfig) (jetstream.Stream, error) {
	return nil, errors.New("not implemented")
}
func (js *stubJetStream) UpdateStream(_ context.Context, _ jetstream.StreamConfig) (jetstream.Stream, error) {
	return nil, errors.New("not implemented")
}
func (js *stubJetStream) CreateOrUpdateStream(_ context.Context, _ jetstream.StreamConfig) (jetstream.Stream, error) {
	return nil, errors.New("not implemented")
}
func (js *stubJetStream) DeleteStream(_ context.Context, _ string) error {
	return errors.New("not implemented")
}
func (js *stubJetStream) ListStreams(_ context.Context) jetstream.StreamInfoLister { return nil }
func (js *stubJetStream) StreamNames(_ context.Context) jetstream.StreamNameLister { return nil }
func (js *stubJetStream) AccountInfo(_ context.Context) (*jetstream.AccountInfo, error) {
	return nil, errors.New("not implemented")
}
func (js *stubJetStream) Publish(_ context.Context, _ string, _ []byte, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	return nil, errors.New("not implemented")
}
func (js *stubJetStream) PublishMsg(_ context.Context, _ *nats.Msg, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	return nil, errors.New("not implemented")
}
func (js *stubJetStream) PublishAsync(_ string, _ []byte, _ ...jetstream.PublishOpt) (jetstream.PubAckFuture, error) {
	return nil, errors.New("not implemented")
}
func (js *stubJetStream) PublishMsgAsync(_ *nats.Msg, _ ...jetstream.PublishOpt) (jetstream.PubAckFuture, error) {
	return nil, errors.New("not implemented")
}
func (js *stubJetStream) PublishAsyncPending() int              { return 0 }
func (js *stubJetStream) PublishAsyncComplete() <-chan struct{} { return nil }
func (js *stubJetStream) KeyValue(_ context.Context, _ string) (jetstream.KeyValue, error) {
	return nil, errors.New("not implemented")
}
func (js *stubJetStream) CreateKeyValue(_ context.Context, _ jetstream.KeyValueConfig) (jetstream.KeyValue, error) {
	return nil, errors.New("not implemented")
}
func (js *stubJetStream) DeleteKeyValue(_ context.Context, _ string) error {
	return errors.New("not implemented")
}
func (js *stubJetStream) ObjectStore(_ context.Context, _ string) (jetstream.ObjectStore, error) {
	return nil, errors.New("not implemented")
}
func (js *stubJetStream) CreateObjectStore(_ context.Context, _ jetstream.ObjectStoreConfig) (jetstream.ObjectStore, error) {
	return nil, errors.New("not implemented")
}
func (js *stubJetStream) DeleteObjectStore(_ context.Context, _ string) error {
	return errors.New("not implemented")
}
func (js *stubJetStream) StreamNameBySubject(_ context.Context, _ string) (string, error) {
	return "", errors.New("not implemented")
}

// ---------------------------------------------------------------------------
// Helper to create a NATSToolBridge wired with stubs (no real NATS conn)
// ---------------------------------------------------------------------------

func newTestBridge(js jetstream.JetStream, mgr DaemonConnectionManager) *NATSToolBridge {
	ctx, cancel := context.WithCancel(context.Background())
	return &NATSToolBridge{
		nc:            nil, // not needed for drainPendingCommands
		js:            js,
		mgr:           mgr,
		daemonSubs:    make(map[string][]*nats.Subscription),
		daemonCancels: make(map[string]context.CancelFunc),
		ctx:           ctx,
		cancel:        cancel,
	}
}

// makePendingMsg creates a stubMsg with a JSON-encoded command envelope.
func makePendingMsg(t *testing.T, requestID, commandType string, payload json.RawMessage, timeoutMs int32) *stubMsg {
	t.Helper()
	data, err := json.Marshal(struct {
		RequestID   string          `json:"request_id"`
		CommandType string          `json:"command_type"`
		Payload     json.RawMessage `json:"payload"`
		TimeoutMs   int32           `json:"timeout_ms"`
	}{
		RequestID:   requestID,
		CommandType: commandType,
		Payload:     payload,
		TimeoutMs:   timeoutMs,
	})
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

	// Should return immediately without panic or error.
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

	// Should log and return gracefully — no crash.
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

	// Non-ErrStreamNotFound errors should also be handled gracefully.
	bridge.drainPendingCommands(context.Background(), "user-1", "daemon-1")

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	require.Empty(t, mgr.commands)
}

func TestDrainPendingCommands_NoPendingMessages(t *testing.T) {
	mgr := &recordingDaemonMgr{}
	consumer := &stubConsumer{
		batches: []jetstream.MessageBatch{
			// First fetch returns empty batch with iterator-closed.
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
				err:  jetstream.ErrMsgIteratorClosed, // signals end of batch
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

func TestDrainPendingCommands_AcksEvenOnDispatchError(t *testing.T) {
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

	// Message should still be acked even though dispatch failed.
	assert.True(t, msg1.wasAcked(), "msg should be acked even on dispatch error")
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

	// Bad message acked, good message dispatched and acked.
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
			// First batch has one message, no terminal error.
			&stubMessageBatch{msgs: []jetstream.Msg{msg1}},
			// Second batch has another message, then signals done.
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
