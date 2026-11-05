package cli

import (
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// `option idempotency_level = …;` reaches codegen.Method.NoSideEffects
// through the real protogen round trip, and only NO_SIDE_EFFECTS sets it:
// IDEMPOTENT is a write that is safe to repeat, and the unset default
// promises nothing, so neither may turn an rpc into a cached read.
func TestExtractService_RecordsNoSideEffects(t *testing.T) {
	level := func(l descriptorpb.MethodOptions_IdempotencyLevel) *descriptorpb.MethodOptions {
		return &descriptorpb.MethodOptions{IdempotencyLevel: l.Enum()}
	}
	method := func(name string, opts *descriptorpb.MethodOptions) *descriptorpb.MethodDescriptorProto {
		return &descriptorpb.MethodDescriptorProto{
			Name:       proto.String(name),
			InputType:  proto.String(".system.v1.Empty"),
			OutputType: proto.String(".system.v1.Empty"),
			Options:    opts,
		}
	}
	file := &descriptorpb.FileDescriptorProto{
		Name:        proto.String("system/v1/system.proto"),
		Package:     proto.String("system.v1"),
		Syntax:      proto.String("proto3"),
		Options:     &descriptorpb.FileOptions{GoPackage: proto.String("example.com/gen/system/v1;systemv1")},
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Empty")}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("SystemService"),
			Method: []*descriptorpb.MethodDescriptorProto{
				method("Version", level(descriptorpb.MethodOptions_NO_SIDE_EFFECTS)),
				method("SetFlag", level(descriptorpb.MethodOptions_IDEMPOTENT)),
				method("Explicit", level(descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN)),
				method("Ready", nil),
			},
		}},
	}
	p, err := protogen.Options{}.New(&pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{file.GetName()},
		ProtoFile:      []*descriptorpb.FileDescriptorProto{file},
	})
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	f := p.FilesByPath[file.GetName()]

	want := map[string]bool{"Version": true, "SetFlag": false, "Explicit": false, "Ready": false}
	levels := map[string]string{"Version": "NO_SIDE_EFFECTS", "SetFlag": "IDEMPOTENT", "Explicit": "IDEMPOTENCY_UNKNOWN", "Ready": "IDEMPOTENCY_UNKNOWN"}
	for _, m := range extractService(f, f.Services[0]).Methods {
		if m.Idempotency != levels[m.Name] {
			t.Errorf("%s.Idempotency = %q, want %q", m.Name, m.Idempotency, levels[m.Name])
		}
		if m.NoSideEffects != want[m.Name] {
			t.Errorf("%s.NoSideEffects = %v, want %v", m.Name, m.NoSideEffects, want[m.Name])
		}
	}
}
