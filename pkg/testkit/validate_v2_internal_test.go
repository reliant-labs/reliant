package testkit

import (
	"context"
	"strings"
	"sync"
	"testing"

	connectv2 "connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakeStreamV2 is a ServerStream whose Receive fills in a prepared message.
type fakeStreamV2 struct {
	connectv2.ServerStream
	msg proto.Message
}

func (s fakeStreamV2) Receive(msg any) error {
	proto.Merge(msg.(proto.Message), s.msg)
	return nil
}

// The v2 warning interceptor logs a violating request on the test — naming
// the procedure's rule and the v2 opt-in — and lets every request through.
func TestWarnInvalidRequestsV2_LogsAndLetsTheRequestThrough(t *testing.T) {
	md := ruledMessage(t)
	rec := &logRecorder{TB: t}
	w := newWarnInvalidRequestsV2(rec)
	if w == nil {
		t.Fatal("no validator: the v2 test server would not check requests at all")
	}
	called := 0
	handler := w(func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		called++
		return stream.Receive(dynamicpb.NewMessage(md))
	})
	spec := connectv2.Spec{Procedure: "/testkit.validatetest.v1.ThingService/CreateThing"}
	valid := dynamicpb.NewMessage(md)
	valid.Set(md.Fields().ByName("name"), protoreflect.ValueOfString("widget"))
	for _, msg := range []proto.Message{dynamicpb.NewMessage(md), valid} {
		if err := handler(context.Background(), spec, fakeStreamV2{msg: msg}); err != nil {
			t.Fatalf("warn mode must not reject: %v", err)
		}
	}
	if called != 2 {
		t.Errorf("handler ran %d times, want 2", called)
	}
	if len(rec.logs) != 1 {
		t.Fatalf("logged %d warnings, want 1 (only the invalid request): %q", len(rec.logs), rec.logs)
	}
	for _, want := range []string{"breaks its protovalidate rules", "name", "invalid_argument", "testkit.ValidateRequestsV2()"} {
		if !strings.Contains(rec.logs[0], want) {
			t.Errorf("warning %q does not say %q", rec.logs[0], want)
		}
	}
}

// Over the wire: by default the v2 test server lets an invalid request reach
// the handler; with ValidateRequestsV2() it answers invalid_argument before
// the handler runs, as production's chain does.
func TestValidateRequestsV2_RejectsLikeProduction(t *testing.T) {
	md := ruledMessage(t)
	spec := connectv2.Spec{Procedure: "/testkit.validatetest.v1.ThingService/CreateThing", StreamType: connectv2.StreamTypeUnary}
	var mu sync.Mutex
	calls := 0
	register := func(server *connectv2.Server) {
		server.Register(connectv2.Method{Spec: spec, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
			if err := stream.Receive(dynamicpb.NewMessage(md)); err != nil {
				return err
			}
			mu.Lock()
			calls++
			mu.Unlock()
			return stream.Send(&structpb.Struct{})
		}})
	}
	call := func(c *connectv2.Client) error {
		return c.CallUnary(context.Background(), spec, dynamicpb.NewMessage(md), &structpb.Struct{})
	}
	asClient := func(c *connectv2.Client) *connectv2.Client { return c }

	_, warned := NewConnectClientV2(t, register, asClient)
	if err := call(warned); err != nil {
		t.Fatalf("while the rollout warns, an invalid request still reaches the handler: %v", err)
	}
	_, enforced := NewConnectClientV2(t, register, asClient, ValidateRequestsV2())
	if err := call(enforced); connectv2.CodeOf(err) != connectv2.CodeInvalidArgument {
		t.Fatalf("ValidateRequestsV2(): got %v, want invalid_argument", err)
	}
	if calls != 1 {
		t.Errorf("handler ran %d times, want 1: ValidateRequestsV2() rejects before the handler", calls)
	}
}
