package validate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	validatepb "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// transferDesc is, built at test time, the descriptor of
//
//	message Transfer { int64 amount_cents = 1 [(buf.validate.field).int64.gte = 0]; }
//
// so these tests run the REAL protovalidate engine against a rule declared the
// way a service proto declares it, with no generated test proto to maintain.
func transferDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	transferOnce.Do(func() { transferMD, transferErr = buildTransferDesc() })
	if transferErr != nil {
		t.Fatalf("build the rule-carrying descriptor: %v", transferErr)
	}
	return transferMD
}

var (
	transferOnce sync.Once
	transferMD   protoreflect.MessageDescriptor
	transferErr  error
)

func buildTransferDesc() (protoreflect.MessageDescriptor, error) {
	opts := &descriptorpb.FieldOptions{}
	gte := int64(0)
	proto.SetExtension(opts, validatepb.E_Field, &validatepb.FieldRules{
		Type: &validatepb.FieldRules_Int64{Int64: &validatepb.Int64Rules{
			GreaterThan: &validatepb.Int64Rules_Gte{Gte: gte},
		}},
	})
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("validate_test/transfer.proto"),
		Package:    proto.String("validatetest.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"buf/validate/validate.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Transfer"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("amount_cents"),
				JsonName: proto.String("amountCents"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum(),
				Options:  opts,
			}},
		}},
	}
	file, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		return nil, err
	}
	return file.Messages().ByName("Transfer"), nil
}

func transfer(t *testing.T, cents int64) *dynamicpb.Message {
	t.Helper()
	md := transferDesc(t)
	m := dynamicpb.NewMessage(md)
	m.Set(md.Fields().ByName("amount_cents"), protoreflect.ValueOfInt64(cents))
	return m
}

func TestInterceptor_EnforcesADeclaredRuleOnUnary(t *testing.T) {
	ic, err := Interceptor()
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		ran++
		return nil, nil
	})
	wrapped := ic.WrapUnary(next)

	_, err = wrapped(context.Background(), connect.NewRequest(transfer(t, -5)))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a negative amount: err = %v, want InvalidArgument", err)
	}
	if !strings.Contains(err.Error(), "amount_cents") {
		t.Errorf("the violation does not name the field: %v", err)
	}
	if ran != 0 {
		t.Fatal("the handler ran for an invalid request")
	}

	if _, err := wrapped(context.Background(), connect.NewRequest(transfer(t, 0))); err != nil {
		t.Fatalf("the boundary value 0 satisfies gte=0: %v", err)
	}
	if _, err := wrapped(context.Background(), connect.NewRequest(transfer(t, 100))); err != nil {
		t.Fatalf("a valid amount: %v", err)
	}
	if ran != 2 {
		t.Fatalf("handler ran %d times, want 2", ran)
	}
}

// A request that is not a protobuf message carries no rules, so it passes.
func TestInterceptor_NonProtoRequestPassesThrough(t *testing.T) {
	ic, err := Interceptor()
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		ran = true
		return nil, nil
	})
	if _, err := ic.WrapUnary(next)(context.Background(), connect.NewRequest(&struct{ N int }{N: -1})); err != nil || !ran {
		t.Fatalf("err = %v, ran = %v; want the handler to run", err, ran)
	}
	v2 := &interceptor{v: fakeValidator{err: errors.New("would reject a proto")}}
	if err := v2.validateV2(&struct{ N int }{}); err != nil {
		t.Fatalf("validateV2 of a non-proto = %v, want nil", err)
	}
}

// Request validation is a server concern: a client stream passes untouched.
func TestInterceptor_WrapStreamingClientIsAPassThrough(t *testing.T) {
	ic, err := Interceptor()
	if err != nil {
		t.Fatal(err)
	}
	called := false
	next := connect.StreamingClientFunc(func(_ context.Context, s connect.Spec) connect.StreamingClientConn {
		called = true
		if s.Procedure != "/p" {
			t.Errorf("spec changed: %v", s.Procedure)
		}
		return nil
	})
	ic.WrapStreamingClient(next)(context.Background(), connect.Spec{Procedure: "/p"})
	if !called {
		t.Fatal("WrapStreamingClient did not hand the call to next")
	}
}

// streamConn is a StreamingHandlerConn whose Receive plays back a script.
type streamConn struct {
	connect.StreamingHandlerConn
	script []func(msg any) error
}

func (c *streamConn) Receive(msg any) error {
	step := c.script[0]
	c.script = c.script[1:]
	return step(msg)
}

func fill(m *dynamicpb.Message) func(any) error {
	return func(msg any) error {
		dst := msg.(*dynamicpb.Message)
		m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			dst.Set(fd, v)
			return true
		})
		return nil
	}
}

func TestInterceptor_WrapStreamingHandlerValidatesEachReceivedMessage(t *testing.T) {
	ic, err := Interceptor()
	if err != nil {
		t.Fatal(err)
	}
	conn := &streamConn{script: []func(any) error{
		fill(transfer(t, 10)),
		fill(transfer(t, -1)),
		func(any) error { return io.EOF },
	}}
	var got []error
	handler := connect.StreamingHandlerFunc(func(_ context.Context, c connect.StreamingHandlerConn) error {
		for range 3 {
			got = append(got, c.Receive(dynamicpb.NewMessage(transferDesc(t))))
		}
		return nil
	})
	if err := ic.WrapStreamingHandler(handler)(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	if got[0] != nil {
		t.Errorf("a valid message: %v", got[0])
	}
	if connect.CodeOf(got[1]) != connect.CodeInvalidArgument {
		t.Errorf("an invalid message: %v, want InvalidArgument", got[1])
	}
	// A transport error is returned as-is, never mistaken for a violation.
	if !errors.Is(got[2], io.EOF) {
		t.Errorf("a receive error: %v, want io.EOF", got[2])
	}
}

// The v2 interceptor, over the wire, enforces the same declared rule and
// carries the violation text to the client.
func TestInterceptorV2_EnforcesADeclaredRuleOverTheWire(t *testing.T) {
	ic, err := InterceptorV2()
	if err != nil {
		t.Fatal(err)
	}
	md := transferDesc(t)
	spec := connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: "/validatetest.v1.Bank/Transfer"}
	ran := 0
	server := connectv2.NewServer(ic)
	server.Register(connectv2.Method{Spec: spec, Handler: func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		req := dynamicpb.NewMessage(md)
		if err := stream.Receive(req); err != nil {
			return err
		}
		ran++
		return stream.Send(dynamicpb.NewMessage(md))
	}})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := connectv2.NewClient(connecthttp.NewTransport(srv.Client(), srv.URL))

	err = client.CallUnary(context.Background(), spec, transfer(t, -9), dynamicpb.NewMessage(md))
	var ce *connectv2.Error
	if !errors.As(err, &ce) || ce.Code() != connectv2.CodeInvalidArgument || !strings.Contains(ce.Message(), "amount_cents") {
		t.Fatalf("a negative amount: %v, want invalid_argument naming amount_cents", err)
	}
	if ran != 0 {
		t.Fatal("the handler ran for an invalid request")
	}
	if err := client.CallUnary(context.Background(), spec, transfer(t, 9), dynamicpb.NewMessage(md)); err != nil || ran != 1 {
		t.Fatalf("a valid amount: err = %v, ran = %d", err, ran)
	}
}

// A receive failure on the v2 stream is the transport's, returned untouched.
func TestInterceptorV2_ReceiveErrorsPassThrough(t *testing.T) {
	i := &interceptor{v: fakeValidator{err: errors.New("never consulted")}}
	boom := errors.New("connection reset")
	var got error
	handler := connectv2.ServerFunc(func(_ context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		got = stream.Receive(&dynamicpb.Message{})
		return nil
	})
	stream := &failingStream{err: boom}
	if err := i.serverV2(handler)(context.Background(), unarySpec, stream); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(got, boom) {
		t.Fatalf("Receive = %v, want the transport error", got)
	}
}

type failingStream struct {
	connectv2.ServerStream
	err error
}

func (s *failingStream) Receive(any) error { return s.err }
