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
	data  []byte
	mu    sync.Mutex
	acked bool
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
func (m *stubMsg) DoubleAck(context.Context) error  { return nil }
func (m *stubMsg) Nak() error                       { return nil }
func (m *stubMsg) NakWithDelay(time.Duration) error { return nil }
func (m *stubMsg) InProgress() error                { return nil }
func (m *stubMsg) Term() error                      { return nil }
func (m *stubMsg) TermWithReason(string) error      { return nil }
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

// stubStream implements jetstream.Stream — only OrderedConsumer is used.
type stubStream struct {
	consumer jetstream.Consumer
}

func (s *stubStream) OrderedConsumer(_ context.Context, _ jetstream.OrderedConsumerConfig) (jetstream.Consumer, error) {
	return s.consumer, nil
}

func (s *stubStream) CreateOrUpdateConsumer(context.Context, jetstream.ConsumerConfig) (jetstream.Consumer, error) {
	panic("not implemented")
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
func (s *stubStream) DeleteConsumer(context.Context, string) error { return nil }
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
