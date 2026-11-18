package cli

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/reliant-labs/forge/internal/codegen"
	forgev1 "github.com/reliant-labs/forge/pkg/forgepb"
)

// TestMethodActivity_RecordsTheResolversVerdict pins that the descriptor
// carries pkg/activity's verdict for each rpc — the record the lint
// forgeconv-activity-declared judges — for the four outcomes it
// distinguishes.
func TestMethodActivity_RecordsTheResolversVerdict(t *testing.T) {
	str := func(name string, n int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(n), JsonName: proto.String(name),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:  descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
		}
	}
	withActivity := func(a *forgev1.ActivityOptions) *descriptorpb.MethodOptions {
		o := &descriptorpb.MethodOptions{}
		proto.SetExtension(o, forgev1.E_Method, &forgev1.MethodOptions{Activity: a})
		return o
	}
	rpc := func(name string, opts *descriptorpb.MethodOptions) *descriptorpb.MethodDescriptorProto {
		return &descriptorpb.MethodDescriptorProto{Name: proto.String(name), InputType: proto.String(".t.v1.Req"), OutputType: proto.String(".t.v1.Req"), Options: opts}
	}
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("t/v1/t.proto"), Package: proto.String("t.v1"), Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Req"), Field: []*descriptorpb.FieldDescriptorProto{str("id", 1)}}},
		Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("S"), Method: []*descriptorpb.MethodDescriptorProto{
			rpc("GetThing", nil),
			rpc("Promote", withActivity(&forgev1.ActivityOptions{Verb: "promote", Target: &forgev1.ActivityTarget{Kind: "release", Id: "id"}})),
			rpc("Preview", withActivity(&forgev1.ActivityOptions{None: "dry run"})),
			rpc("Rotate", nil),
			rpc("Restart", withActivity(&forgev1.ActivityOptions{Verb: "restart", Target: &forgev1.ActivityTarget{Kind: "workload", Id: "nope"}})),
		}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	methods := fd.Services().Get(0).Methods()
	got := func(name string) *codegen.MethodActivity {
		return methodActivity(methods.ByName(protoreflect.Name(name)))
	}

	if a := got("GetThing"); a.Mutating || a.Declared {
		t.Errorf("GetThing = %+v, want a non-mutating rpc with nothing declared", a)
	}
	if a := got("Promote"); !a.Mutating || !a.Declared || a.Verb != "promote" || a.TargetKind != "release" || a.Writer != "INTERCEPTOR" {
		t.Errorf("Promote = %+v", a)
	}
	if a := got("Preview"); !a.Declared || a.None != "dry run" {
		t.Errorf("Preview = %+v", a)
	}
	if a := got("Rotate"); !a.Mutating || a.Declared || a.Error != "" {
		t.Errorf("Rotate = %+v, want mutating and undeclared", a)
	}
	if a := got("Restart"); a.Declared || !strings.Contains(a.Error, `has no field "nope"`) {
		t.Errorf("Restart = %+v, want Resolve's path error", a)
	}
}
