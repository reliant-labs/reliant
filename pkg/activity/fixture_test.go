package activity

import (
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// fixtureMethod is one rpc of the fixture service.
type fixtureMethod struct {
	name, in, out string
	activity      *forgepb.ActivityOptions
	noSideEffects bool
	streaming     bool
}

// fixtureMessages is the message set every fixture file declares.
func fixtureMessages() []*descriptorpb.DescriptorProto {
	str := func(name string, n int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(n), JsonName: proto.String(name),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:  descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		}
	}
	msgField := func(name string, n int32, typ string) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(n), JsonName: proto.String(name),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: proto.String(".fixture.v1." + typ),
		}
	}
	repeated := str("tags", 9)
	repeated.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	count := &descriptorpb.FieldDescriptorProto{
		Name: proto.String("replicas"), Number: proto.Int32(5), JsonName: proto.String("replicas"),
		Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:  descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
	}
	return []*descriptorpb.DescriptorProto{
		{Name: proto.String("Release"), Field: []*descriptorpb.FieldDescriptorProto{str("id", 1), str("version", 2)}},
		{Name: proto.String("PromoteRequest"), Field: []*descriptorpb.FieldDescriptorProto{
			str("release_id", 1), str("to_env", 2), str("from_env", 3), str("project_id", 4), count,
			msgField("release", 6, "Release"), repeated,
		}},
		{Name: proto.String("PromoteResponse"), Field: []*descriptorpb.FieldDescriptorProto{msgField("release", 1, "Release")}},
		{Name: proto.String("Widget"), Field: []*descriptorpb.FieldDescriptorProto{str("id", 1), str("name", 2)}},
		{Name: proto.String("CreateWidgetRequest"), Field: []*descriptorpb.FieldDescriptorProto{str("name", 1)}},
		{Name: proto.String("CreateWidgetResponse"), Field: []*descriptorpb.FieldDescriptorProto{msgField("widget", 1, "Widget")}},
		{Name: proto.String("UpdateWidgetRequest"), Field: []*descriptorpb.FieldDescriptorProto{msgField("widget", 1, "Widget")}},
		{Name: proto.String("UpdateWidgetResponse"), Field: []*descriptorpb.FieldDescriptorProto{msgField("widget", 1, "Widget")}},
		{Name: proto.String("DeleteWidgetRequest"), Field: []*descriptorpb.FieldDescriptorProto{str("id", 1)}},
		{Name: proto.String("DeleteWidgetResponse"), Field: []*descriptorpb.FieldDescriptorProto{str("id", 1)}},
	}
}

// fixtureService builds a file declaring the fixture messages and one
// service with methods, and returns the service.
func fixtureService(t *testing.T, methods ...fixtureMethod) protoreflect.ServiceDescriptor {
	t.Helper()
	svc := &descriptorpb.ServiceDescriptorProto{Name: proto.String("FixtureService")}
	for _, m := range methods {
		opts := &descriptorpb.MethodOptions{}
		if m.activity != nil {
			proto.SetExtension(opts, forgepb.E_Method, &forgepb.MethodOptions{Activity: m.activity})
		}
		if m.noSideEffects {
			opts.IdempotencyLevel = descriptorpb.MethodOptions_NO_SIDE_EFFECTS.Enum()
		}
		svc.Method = append(svc.Method, &descriptorpb.MethodDescriptorProto{
			Name:            proto.String(m.name),
			InputType:       proto.String(".fixture.v1." + m.in),
			OutputType:      proto.String(".fixture.v1." + m.out),
			Options:         opts,
			ServerStreaming: proto.Bool(m.streaming),
		})
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name:        proto.String(fmt.Sprintf("fixture/v1/%s.proto", t.Name())),
		Package:     proto.String("fixture.v1"),
		Syntax:      proto.String("proto3"),
		MessageType: fixtureMessages(),
		Service:     []*descriptorpb.ServiceDescriptorProto{svc},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatalf("build fixture file: %v", err)
	}
	return fd.Services().Get(0)
}

// fixtureRPC builds a single-method service and returns the method.
func fixtureRPC(t *testing.T, m fixtureMethod) protoreflect.MethodDescriptor {
	t.Helper()
	if m.in == "" {
		m.in, m.out = "PromoteRequest", "PromoteResponse"
	}
	return fixtureService(t, m).Methods().Get(0)
}

// promoteDecl is the canonical explicit declaration the tests use.
func promoteDecl() *forgepb.ActivityOptions {
	return &forgepb.ActivityOptions{
		Verb:   "promote",
		Target: &forgepb.ActivityTarget{Kind: "release", Id: "release_id", Label: "response.release.version"},
		Env:    "to_env",
		Scope:  "project_id",
		Detail: []string{"from_env", "replicas"},
	}
}
