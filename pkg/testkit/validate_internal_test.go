package testkit

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	validatepb "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/structpb"
)

// logRecorder is a testing.TB that keeps what is logged on it.
type logRecorder struct {
	testing.TB
	mu   sync.Mutex
	logs []string
}

func (r *logRecorder) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

// ruledMessage builds a message type whose `name` field carries
// `(buf.validate.field).string.min_len = 1`, the way a project's proto would.
func ruledMessage(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	opts := &descriptorpb.FieldOptions{}
	proto.SetExtension(opts, validatepb.E_Field, &validatepb.FieldRules{
		Type: &validatepb.FieldRules_String_{String_: &validatepb.StringRules{MinLen: proto.Uint64(1)}},
	})
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:       proto.String("testkit/validate_test.proto"),
		Package:    proto.String("testkit.validatetest.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"buf/validate/validate.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("CreateThingRequest"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("name"),
				Number:   proto.Int32(1),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				JsonName: proto.String("name"),
				Options:  opts,
			}},
		}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("build the test descriptor: %v", err)
	}
	return fd.Messages().Get(0)
}

// forge#2283: the test server checks what production checks. While the
// rollout warns, a request breaking its rules is logged on the test, naming
// the procedure, the violation and the opt-in, and the handler still runs.
func TestWarnInvalidRequests_LogsAndLetsTheRequestThrough(t *testing.T) {
	md := ruledMessage(t)
	rec := &logRecorder{TB: t}
	w := newWarnInvalidRequests(rec)
	if w == nil {
		t.Fatal("no validator: the test server would not check requests at all")
	}

	called := 0
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		called++
		return connect.NewResponse(&structpb.Struct{}), nil
	})
	invalid := dynamicpb.NewMessage(md) // name == "": breaks min_len = 1
	if _, err := w.WrapUnary(next)(context.Background(), connect.NewRequest(invalid)); err != nil {
		t.Fatalf("warn mode must not reject: %v", err)
	}
	valid := dynamicpb.NewMessage(md)
	valid.Set(md.Fields().ByName("name"), protoreflect.ValueOfString("widget"))
	if _, err := w.WrapUnary(next)(context.Background(), connect.NewRequest(valid)); err != nil {
		t.Fatalf("a valid request failed: %v", err)
	}

	if called != 2 {
		t.Errorf("handler ran %d times, want 2: warn mode lets every request through", called)
	}
	if len(rec.logs) != 1 {
		t.Fatalf("logged %d warnings, want 1 (only the invalid request): %q", len(rec.logs), rec.logs)
	}
	for _, want := range []string{"breaks its protovalidate rules", "name", "invalid_argument", "testkit.ValidateRequests()"} {
		if !strings.Contains(rec.logs[0], want) {
			t.Errorf("warning %q does not say %q", rec.logs[0], want)
		}
	}
}

// Over the wire: by default the test server lets an invalid request reach the
// handler (and warns); with ValidateRequests() it answers invalid_argument
// before the handler runs, as production's chain does.
func TestValidateRequests_RejectsLikeProduction(t *testing.T) {
	md := ruledMessage(t)
	const procedure = "/testkit.validatetest.v1.ThingService/CreateThing"
	var calls int
	var mu sync.Mutex
	register := func(mux *http.ServeMux, opts ...connect.HandlerOption) {
		opts = append(opts, connect.WithRequestInitializer(func(_ connect.Spec, msg any) error {
			if dm, ok := msg.(*dynamicpb.Message); ok {
				*dm = *dynamicpb.NewMessage(md)
			}
			return nil
		}))
		mux.Handle(procedure, connect.NewUnaryHandler(procedure,
			func(context.Context, *connect.Request[dynamicpb.Message]) (*connect.Response[structpb.Struct], error) {
				mu.Lock()
				calls++
				mu.Unlock()
				return connect.NewResponse(&structpb.Struct{}), nil
			}, opts...))
	}
	newClient := func(hc connect.HTTPClient, url string, opts ...connect.ClientOption) *connect.Client[dynamicpb.Message, structpb.Struct] {
		return connect.NewClient[dynamicpb.Message, structpb.Struct](hc, url+procedure, opts...)
	}

	_, warned := NewConnectClient(t, register, newClient)
	if _, err := warned.CallUnary(context.Background(), connect.NewRequest(dynamicpb.NewMessage(md))); err != nil {
		t.Fatalf("while the rollout warns, an invalid request still reaches the handler: %v", err)
	}

	_, enforced := NewConnectClient(t, register, newClient, ValidateRequests())
	_, err := enforced.CallUnary(context.Background(), connect.NewRequest(dynamicpb.NewMessage(md)))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("ValidateRequests(): got %v, want invalid_argument", err)
	}
	if calls != 1 {
		t.Errorf("handler ran %d times, want 1: ValidateRequests() rejects before the handler", calls)
	}
}

// Through NewConnectClient: the warning interceptor sits inside the caller's
// options, so a caller's ValidateRequests() rejects before it, and a valid
// call is unaffected.
func TestNewConnectClient_InstallsTheWarningInterceptor(t *testing.T) {
	var seen []string
	prev := requestValidator
	requestValidator = func() (protovalidate.Validator, error) { return recordingValidator{seen: &seen}, nil }
	t.Cleanup(func() { requestValidator = prev })

	const procedure = "/testkit.validatetest.v1.EchoService/Echo"
	register := func(mux *http.ServeMux, opts ...connect.HandlerOption) {
		mux.Handle(procedure, connect.NewUnaryHandler(procedure,
			func(_ context.Context, req *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
				return connect.NewResponse(req.Msg), nil
			}, opts...))
	}
	newClient := func(hc connect.HTTPClient, url string, opts ...connect.ClientOption) *connect.Client[structpb.Struct, structpb.Struct] {
		return connect.NewClient[structpb.Struct, structpb.Struct](hc, url+procedure, opts...)
	}
	_, client := NewConnectClient(t, register, newClient)
	if _, err := client.CallUnary(context.Background(), connect.NewRequest(&structpb.Struct{})); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Errorf("the test server validated %d requests, want 1", len(seen))
	}
}

// recordingValidator accepts everything and records what it was asked about.
type recordingValidator struct{ seen *[]string }

func (r recordingValidator) Validate(m proto.Message, _ ...protovalidate.ValidationOption) error {
	*r.seen = append(*r.seen, string(m.ProtoReflect().Descriptor().FullName()))
	return nil
}
