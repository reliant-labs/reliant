package cli

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/reliant-labs/forge/internal/codegen"
)

// buildDemoPlugin constructs a real protogen graph (via a synthetic
// CodeGeneratorRequest) for one proto file exercising every schema
// shape the deep extractor must handle: nested message, repeated,
// map<string, message>, enum, oneof, proto3 optional, self-recursive
// message, and a google.protobuf.Timestamp well-known field.
//
// Equivalent proto source:
//
//	syntax = "proto3";
//	package demo.v1;
//	import "google/protobuf/timestamp.proto";
//
//	enum Status { STATUS_UNSPECIFIED = 0; STATUS_ACTIVE = 1; }
//	message Node { string label = 1; repeated Node children = 2; }
//	message Address { string street = 1; optional string zip = 2; }
//	message CreateRequest {
//	  string name = 1;
//	  Address address = 2;
//	  repeated string tags = 3;
//	  map<string, Address> homes = 4;
//	  Status status = 5;
//	  google.protobuf.Timestamp created_at = 6;
//	  Node root = 7;
//	  oneof target { string email = 8; string phone = 9; }
//	}
//	message CreateResponse { string id = 1; }
//	service DemoService { rpc Create(CreateRequest) returns (CreateResponse); }
func buildDemoPlugin(t *testing.T) *protogen.Plugin {
	t.Helper()

	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	enu := descriptorpb.FieldDescriptorProto_TYPE_ENUM
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	rep := descriptorpb.FieldDescriptorProto_LABEL_REPEATED

	field := func(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type, label descriptorpb.FieldDescriptorProto_Label, typeName string) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{
			Name:   proto.String(name),
			Number: proto.Int32(num),
			Type:   typ.Enum(),
			Label:  label.Enum(),
		}
		if typeName != "" {
			f.TypeName = proto.String(typeName)
		}
		return f
	}

	// Address: street + optional zip (proto3 optional = synthetic oneof).
	zip := field("zip", 2, str, opt, "")
	zip.Proto3Optional = proto.Bool(true)
	zip.OneofIndex = proto.Int32(0)
	address := &descriptorpb.DescriptorProto{
		Name:      proto.String("Address"),
		Field:     []*descriptorpb.FieldDescriptorProto{field("street", 1, str, opt, ""), zip},
		OneofDecl: []*descriptorpb.OneofDescriptorProto{{Name: proto.String("_zip")}},
	}

	// Node: self-recursive.
	node := &descriptorpb.DescriptorProto{
		Name: proto.String("Node"),
		Field: []*descriptorpb.FieldDescriptorProto{
			field("label", 1, str, opt, ""),
			field("children", 2, msg, rep, ".demo.v1.Node"),
		},
	}

	// CreateRequest with the kitchen sink. Map fields compile to a
	// repeated synthetic <Field>Entry message with map_entry=true.
	homesEntry := &descriptorpb.DescriptorProto{
		Name: proto.String("HomesEntry"),
		Field: []*descriptorpb.FieldDescriptorProto{
			field("key", 1, str, opt, ""),
			field("value", 2, msg, opt, ".demo.v1.Address"),
		},
		Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
	}
	email := field("email", 8, str, opt, "")
	email.OneofIndex = proto.Int32(0)
	phone := field("phone", 9, str, opt, "")
	phone.OneofIndex = proto.Int32(0)
	createReq := &descriptorpb.DescriptorProto{
		Name: proto.String("CreateRequest"),
		Field: []*descriptorpb.FieldDescriptorProto{
			field("name", 1, str, opt, ""),
			field("address", 2, msg, opt, ".demo.v1.Address"),
			field("tags", 3, str, rep, ""),
			field("homes", 4, msg, rep, ".demo.v1.CreateRequest.HomesEntry"),
			field("status", 5, enu, opt, ".demo.v1.Status"),
			field("created_at", 6, msg, opt, ".google.protobuf.Timestamp"),
			field("root", 7, msg, opt, ".demo.v1.Node"),
			email,
			phone,
		},
		NestedType: []*descriptorpb.DescriptorProto{homesEntry},
		OneofDecl:  []*descriptorpb.OneofDescriptorProto{{Name: proto.String("target")}},
	}

	createResp := &descriptorpb.DescriptorProto{
		Name:  proto.String("CreateResponse"),
		Field: []*descriptorpb.FieldDescriptorProto{field("id", 1, str, opt, "")},
	}

	demoFile := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("demo/v1/demo.proto"),
		Package:    proto.String("demo.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/timestamp.proto"},
		Options: &descriptorpb.FileOptions{
			GoPackage: proto.String("example.com/gen/demo/v1;demov1"),
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{
				Name: proto.String("Status"),
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: proto.String("STATUS_UNSPECIFIED"), Number: proto.Int32(0)},
					{Name: proto.String("STATUS_ACTIVE"), Number: proto.Int32(1)},
				},
			},
		},
		MessageType: []*descriptorpb.DescriptorProto{address, node, createReq, createResp},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: proto.String("DemoService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       proto.String("Create"),
						InputType:  proto.String(".demo.v1.CreateRequest"),
						OutputType: proto.String(".demo.v1.CreateResponse"),
					},
				},
			},
		},
	}

	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{demoFile.GetName()},
		ProtoFile: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(timestamppb.File_google_protobuf_timestamp_proto),
			demoFile,
		},
	}
	p, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	return p
}

// TestExtractService_DeepSchemaGraph verifies the descriptor side of
// deep schemas: extractService must record the full reachable type
// graph (keyed by fully-qualified name), enum value lists, oneof
// membership, optional flags, map key/value typing, and the method's
// fully-qualified input/output names — while NOT recording well-known
// google.protobuf.* internals.
func TestExtractService_DeepSchemaGraph(t *testing.T) {
	p := buildDemoPlugin(t)
	var file *protogen.File
	for _, f := range p.Files {
		if f.Generate {
			file = f
		}
	}
	if file == nil {
		t.Fatal("no generated file in plugin")
	}

	sd := extractService(file, file.Services[0])

	// Method-level FQ names.
	m := sd.Methods[0]
	if m.InputTypeFQ != "demo.v1.CreateRequest" || m.OutputTypeFQ != "demo.v1.CreateResponse" {
		t.Errorf("FQ types = %q/%q, want demo.v1.CreateRequest/demo.v1.CreateResponse", m.InputTypeFQ, m.OutputTypeFQ)
	}

	// The reachable graph: request, response, Address, Node. NOT
	// Timestamp (well-known) and NOT the synthetic HomesEntry.
	for _, fq := range []string{"demo.v1.CreateRequest", "demo.v1.CreateResponse", "demo.v1.Address", "demo.v1.Node"} {
		if _, ok := sd.Schemas[fq]; !ok {
			t.Errorf("Schemas missing %s (have %v)", fq, keysOf(sd.Schemas))
		}
	}
	if _, ok := sd.Schemas["google.protobuf.Timestamp"]; ok {
		t.Error("well-known Timestamp must not be recorded in Schemas")
	}

	// Enum registry.
	if got := sd.Enums["demo.v1.Status"]; !reflect.DeepEqual(got, []string{"STATUS_UNSPECIFIED", "STATUS_ACTIVE"}) {
		t.Errorf("Enums[demo.v1.Status] = %v", got)
	}
	if got := sd.EnumNumbers["demo.v1.Status"]; !reflect.DeepEqual(got, []int32{0, 1}) {
		t.Errorf("EnumNumbers[demo.v1.Status] = %v, want [0 1]", got)
	}

	// Field-level assertions on CreateRequest.
	byName := map[string]codegen.SchemaFieldDef{}
	for _, f := range sd.Schemas["demo.v1.CreateRequest"] {
		byName[f.Name] = f
	}
	if f := byName["address"]; f.Kind != "message" || f.TypeName != "demo.v1.Address" || f.Repeated {
		t.Errorf("address = %+v", f)
	}
	if f := byName["tags"]; f.Kind != "string" || !f.Repeated {
		t.Errorf("tags = %+v", f)
	}
	if f := byName["homes"]; f.Kind != "map" || f.MapKeyKind != "string" || f.MapValueKind != "message" || f.MapValueTypeName != "demo.v1.Address" || f.Repeated {
		t.Errorf("homes = %+v", f)
	}
	if f := byName["status"]; f.Kind != "enum" || f.TypeName != "demo.v1.Status" {
		t.Errorf("status = %+v", f)
	}
	if f := byName["created_at"]; f.Kind != "message" || f.TypeName != "google.protobuf.Timestamp" {
		t.Errorf("created_at = %+v", f)
	}
	if f := byName["root"]; f.Kind != "message" || f.TypeName != "demo.v1.Node" {
		t.Errorf("root = %+v", f)
	}
	// Real oneof members carry the group name; nothing else does.
	if f := byName["email"]; f.Oneof != "target" {
		t.Errorf("email.Oneof = %q, want target", f.Oneof)
	}
	if f := byName["phone"]; f.Oneof != "target" {
		t.Errorf("phone.Oneof = %q, want target", f.Oneof)
	}
	if f := byName["name"]; f.Oneof != "" || f.Optional {
		t.Errorf("name = %+v, want plain required scalar", f)
	}

	// proto3 optional → Optional=true, and its synthetic oneof must
	// NOT leak as a oneof group.
	var zip codegen.SchemaFieldDef
	for _, f := range sd.Schemas["demo.v1.Address"] {
		if f.Name == "zip" {
			zip = f
		}
	}
	if !zip.Optional || zip.Oneof != "" {
		t.Errorf("Address.zip = %+v, want Optional=true with no oneof (synthetic)", zip)
	}

	// Recursive Node terminated: children references Node by name, and
	// Node appears exactly once in the graph.
	var children codegen.SchemaFieldDef
	for _, f := range sd.Schemas["demo.v1.Node"] {
		if f.Name == "children" {
			children = f
		}
	}
	if children.Kind != "message" || children.TypeName != "demo.v1.Node" || !children.Repeated {
		t.Errorf("Node.children = %+v", children)
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
