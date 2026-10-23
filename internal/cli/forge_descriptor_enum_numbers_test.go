package cli

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// TestExtractService_RecordsDeclaredEnumNumbers pins that the descriptor
// carries each enum value's DECLARED wire number, not just its name. With a
// reserved value the two diverge — PENDING is declared second but numbered 2 —
// and the frontend mock fixtures, which emit enums as numbers, can only be
// right if they read the number:
//
//	enum Status {
//	  reserved 1;
//	  reserved "STATUS_DRAFT";
//	  STATUS_UNSPECIFIED = 0;
//	  STATUS_PENDING = 2;
//	  STATUS_ACTIVE = 3;
//	}
func TestExtractService_RecordsDeclaredEnumNumbers(t *testing.T) {
	enu := descriptorpb.FieldDescriptorProto_TYPE_ENUM
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL

	file := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("gapped/v1/gapped.proto"),
		Package: proto.String("gapped.v1"),
		Syntax:  proto.String("proto3"),
		Options: &descriptorpb.FileOptions{GoPackage: proto.String("example.com/gen/gapped/v1;gappedv1")},
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("Status"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("STATUS_UNSPECIFIED"), Number: proto.Int32(0)},
				{Name: proto.String("STATUS_PENDING"), Number: proto.Int32(2)},
				{Name: proto.String("STATUS_ACTIVE"), Number: proto.Int32(3)},
			},
			ReservedRange: []*descriptorpb.EnumDescriptorProto_EnumReservedRange{{Start: proto.Int32(1), End: proto.Int32(1)}},
			ReservedName:  []string{"STATUS_DRAFT"},
		}},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("GetRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("id"), Number: proto.Int32(1), Type: str.Enum(), Label: opt.Enum()},
				},
			},
			{
				Name: proto.String("GetResponse"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("status"), Number: proto.Int32(1), Type: enu.Enum(), Label: opt.Enum(), TypeName: proto.String(".gapped.v1.Status")},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("GappedService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Get"),
				InputType:  proto.String(".gapped.v1.GetRequest"),
				OutputType: proto.String(".gapped.v1.GetResponse"),
			}},
		}},
	}
	p, err := protogen.Options{}.New(&pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{file.GetName()},
		ProtoFile:      []*descriptorpb.FileDescriptorProto{file},
	})
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	var gen *protogen.File
	for _, f := range p.Files {
		if f.Generate {
			gen = f
		}
	}
	if gen == nil {
		t.Fatal("no generated file in plugin")
	}

	sd := extractService(gen, gen.Services[0])

	if got, want := sd.Enums["gapped.v1.Status"], []string{"STATUS_UNSPECIFIED", "STATUS_PENDING", "STATUS_ACTIVE"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Enums[gapped.v1.Status] = %v, want %v", got, want)
	}
	if got, want := sd.EnumNumbers["gapped.v1.Status"], []int32{0, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("EnumNumbers[gapped.v1.Status] = %v, want %v — declaration order is not the wire number "+
			"once a value is reserved", got, want)
	}
}
