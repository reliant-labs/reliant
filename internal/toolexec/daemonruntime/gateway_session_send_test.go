// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// marshalingSender is a stream whose Send fails exactly as connect's does: by
// marshaling the message, which refuses invalid UTF-8 in a proto3 string.
type marshalingSender struct{ sent chan *reliantv1.DaemonMessage }

func (m marshalingSender) Send(msg *reliantv1.DaemonMessage) error {
	if _, err := proto.Marshal(msg); err != nil {
		return err
	}
	m.sent <- proto.Clone(msg).(*reliantv1.DaemonMessage)
	return nil
}

func utf8ToolResponse(id, content string) *reliantv1.DaemonMessage {
	return &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_ToolResponse{
		ToolResponse: &reliantv1.ToolResponse{RequestId: id, Content: content},
	}}
}

func waitSent(t *testing.T, ch <-chan *reliantv1.DaemonMessage) *reliantv1.DaemonMessage {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was sent")
		return nil
	}
}

// One message with invalid UTF-8 used to stop the writer for good: the stream
// stayed up, nothing drained the queue, and the machine's terminals and tools
// were dead until liveness ended the session ~95s later.
func TestRunSender_InvalidUTF8IsRepairedAndTheStreamKeepsSending(t *testing.T) {
	s := newTestSession(4)
	retired := make(chan struct{})
	s.retire = func() { close(retired) }
	out := marshalingSender{sent: make(chan *reliantv1.DaemonMessage, 4)}
	go s.runSender(out)
	defer s.drain(time.Second)

	s.sendCh <- utf8ToolResponse("bad", "ok \xff\xfe done")
	s.sendCh <- utf8ToolResponse("next", "fine")

	first := waitSent(t, out.sent)
	assert.Equal(t, "bad", first.GetToolResponse().GetRequestId())
	// strings.ToValidUTF8 replaces a run of invalid bytes with one U+FFFD.
	assert.Equal(t, "ok \uFFFD done", first.GetToolResponse().GetContent())
	assert.Equal(t, "next", waitSent(t, out.sent).GetToolResponse().GetRequestId())

	select {
	case <-retired:
		t.Fatal("a repaired message must not end the session")
	default:
	}
	assert.True(t, s.open())
}

// A Send that fails for any other reason ends the session at once, so the
// daemon redials instead of queueing into a writer that is gone.
func TestRunSender_UnrepairableFailureRetiresTheSession(t *testing.T) {
	s := newTestSession(4)
	retired := make(chan struct{})
	s.retire = func() { close(retired) }
	go s.runSender(senderFunc(func(*reliantv1.DaemonMessage) error {
		return errors.New("stream torn")
	}))

	s.sendCh <- utf8ToolResponse("x", "fine")

	select {
	case <-retired:
	case <-time.After(5 * time.Second):
		t.Fatal("the session was not retired after a failed Send")
	}
	select {
	case <-s.sendDone:
	case <-time.After(5 * time.Second):
		t.Fatal("runSender did not exit")
	}
	assert.False(t, s.open(), "producers must stop queueing on a session nobody drains")
	assert.ErrorIs(t, s.send(utf8ToolResponse("y", "fine")), errSessionEnded)
}

func TestRepairUTF8_RepairsNestedListsAndMaps(t *testing.T) {
	// Built by hand: structpb.NewStruct refuses invalid UTF-8 itself.
	str := structpb.NewStringValue
	st := &structpb.Struct{Fields: map[string]*structpb.Value{
		"ok":  str("fine"),
		"bad": str("a\xffb"),
		"list": structpb.NewListValue(&structpb.ListValue{Values: []*structpb.Value{
			str("c\xfed"),
			structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"deep": str("e\xfff")}}),
		}}),
		// A key no producer should ever send, but one that must not break Send.
		"key\xff": str("v"),
	}}
	_, err := proto.Marshal(st)
	require.Error(t, err, "the fixture must be unmarshalable before repair")

	require.True(t, repairUTF8(st.ProtoReflect()))

	_, err = proto.Marshal(st)
	require.NoError(t, err)
	assert.Equal(t, "fine", st.Fields["ok"].GetStringValue())
	assert.Equal(t, "a\uFFFDb", st.Fields["bad"].GetStringValue())
	list := st.Fields["list"].GetListValue().GetValues()
	assert.Equal(t, "c\uFFFDd", list[0].GetStringValue())
	assert.Equal(t, "e\uFFFDf", list[1].GetStructValue().GetFields()["deep"].GetStringValue())
	assert.Equal(t, "v", st.Fields["key\uFFFD"].GetStringValue())
	assert.Len(t, st.Fields, 4)

	assert.False(t, repairUTF8(st.ProtoReflect()), "a valid message is left alone")
}
