package cli

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	forgev1 "github.com/reliant-labs/forge/pkg/forgepb"
)

// boundConfigFile is a config.proto declaring one message bound to binary,
// with source info placing the message on line msgLine and (when
// optLine > 0) its binary_config option on optLine — both 1-based, as an
// editor shows them.
func boundConfigFile(binary string, msgLine, optLine int32) *descriptorpb.FileDescriptorProto {
	opts := &descriptorpb.MessageOptions{}
	proto.SetExtension(opts, forgev1.E_BinaryConfig, &forgev1.BinaryConfigOptions{Binary: binary})

	locs := []*descriptorpb.SourceCodeInfo_Location{
		{Path: []int32{4, 0}, Span: []int32{msgLine - 1, 0, msgLine + 3, 1}},
	}
	if optLine > 0 {
		locs = append(locs, &descriptorpb.SourceCodeInfo_Location{
			Path: []int32{4, 0, 7, int32(forgev1.E_BinaryConfig.TypeDescriptor().Number())},
			Span: []int32{optLine - 1, 2, 60},
		})
	}

	return &descriptorpb.FileDescriptorProto{
		Name:       strPtr("proj/config/v1/config.proto"),
		Package:    strPtr("proj.config.v1"),
		Syntax:     strPtr("proto3"),
		Options:    &descriptorpb.FileOptions{GoPackage: strPtr("example.com/proj/gen/proj/config/v1;configv1")},
		Dependency: []string{"forge/v1/forge.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:    strPtr("ServerApiConfig"),
			Options: opts,
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:    strPtr("jwks_url"),
				Number:  proto.Int32(1),
				Type:    descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Label:   descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Options: configFieldOpts("RELIANT_JWKS_URL"),
			}},
		}},
		SourceCodeInfo: &descriptorpb.SourceCodeInfo{Location: locs},
	}
}

// The binding's position reaches the descriptor, so generate can refuse a
// bad binding at the line an author edits: the option's line when the
// compiler recorded one, else the message's.
func TestExtractConfigMessage_RecordsBindingPosition(t *testing.T) {
	cases := []struct {
		name             string
		msgLine, optLine int32
		want             int
	}{
		{"option line preferred", 40, 41, 41},
		{"message line when the option has no position", 40, 0, 40},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := buildConfigPlugin(t, boundConfigFile("x server api", c.msgLine, c.optLine))
			cm, ok := extractConfigMessage(f.Messages[0])
			if !ok {
				t.Fatal("ServerApiConfig not extracted")
			}
			if cm.Binary != "x server api" {
				t.Errorf("Binary = %q", cm.Binary)
			}
			if cm.ProtoFile != "proj/config/v1/config.proto" || cm.Line != c.want {
				t.Errorf("position = %s:%d, want proj/config/v1/config.proto:%d", cm.ProtoFile, cm.Line, c.want)
			}
			// pkg/config imports the package the message generates into,
			// as its go_package declares it.
			if cm.GoImportPath != "example.com/proj/gen/proj/config/v1" {
				t.Errorf("GoImportPath = %q, want example.com/proj/gen/proj/config/v1", cm.GoImportPath)
			}
		})
	}
}

// The command half of the binding reaches the descriptor alongside the
// binary, so the (binary, command) key the generator projects is the one
// the proto declares.
func TestExtractConfigMessage_RecordsCommand(t *testing.T) {
	file := boundConfigFile("reliant", 10, 11)
	proto.SetExtension(file.MessageType[0].Options, forgev1.E_BinaryConfig,
		&forgev1.BinaryConfigOptions{Binary: "reliant", Command: []string{"server", "api"}})
	cm, ok := extractConfigMessage(buildConfigPlugin(t, file).Messages[0])
	if !ok {
		t.Fatal("ServerApiConfig not extracted")
	}
	if cm.Binary != "reliant" || strings.Join(cm.Command, " ") != "server api" {
		t.Errorf("binding = %q %v, want reliant [server api]", cm.Binary, cm.Command)
	}
	if got := cm.ProcessName(); got != "reliant server api" {
		t.Errorf("ProcessName = %q", got)
	}
}
