// Descriptor-extraction tests for component config blocks: a
// message-typed AppConfig field referencing a message with
// (forge.v1.config)-annotated leaves must surface as a block reference
// (ProtoType "message" + MessageType), and the block message itself
// must surface as its own ConfigMessage — including when it's declared
// NESTED inside AppConfig rather than at top level.
package cli

import (
	"testing"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/reliant-labs/forge/internal/codegen"
	forgev1 "github.com/reliant-labs/forge/pkg/forgepb"
)

// buildConfigPlugin compiles a synthetic config.proto (with the real
// forge/v1/forge.proto as dependency, so the config extension resolves)
// into a protogen.Plugin and returns its generated file.
func buildConfigPlugin(t *testing.T, file *descriptorpb.FileDescriptorProto) *protogen.File {
	t.Helper()

	deps := []*descriptorpb.FileDescriptorProto{
		protodesc.ToFileDescriptorProto(descriptorpb.File_google_protobuf_descriptor_proto),
		protodesc.ToFileDescriptorProto(durationpb.File_google_protobuf_duration_proto),
		protodesc.ToFileDescriptorProto(forgev1.File_forge_v1_forge_proto),
	}
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{file.GetName()},
		ProtoFile:      append(deps, file),
	}
	plugin, err := protogen.Options{}.New(req)
	if err != nil {
		t.Fatalf("protogen.New: %v", err)
	}
	for _, f := range plugin.Files {
		if f.Generate {
			return f
		}
	}
	t.Fatal("no file marked Generate in plugin request")
	return nil
}

// configFieldOpts builds FieldOptions carrying (forge.v1.config).
func configFieldOpts(envVar string) *descriptorpb.FieldOptions {
	opts := &descriptorpb.FieldOptions{}
	proto.SetExtension(opts, forgev1.E_Config, &forgev1.ConfigFieldOptions{
		EnvVar: envVar,
	})
	return opts
}

func strPtr(s string) *string { return &s }

func TestExtractConfigMessage_BlockReference(t *testing.T) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    strPtr("config/v1/config.proto"),
		Package: strPtr("config.v1"),
		Syntax:  strPtr("proto3"),
		Options: &descriptorpb.FileOptions{
			GoPackage: strPtr("example.com/proj/gen/config/v1;configv1"),
		},
		Dependency: []string{"forge/v1/forge.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: strPtr("TraderConfig"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:    strPtr("max_per_tick"),
						Number:  proto.Int32(1),
						Type:    descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
						Label:   descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Options: configFieldOpts("TRADER_MAX_PER_TICK"),
					},
				},
			},
			{
				Name: strPtr("AppConfig"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:    strPtr("port"),
						Number:  proto.Int32(1),
						Type:    descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
						Label:   descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Options: configFieldOpts("PORT"),
					},
					{
						Name:     strPtr("trader"),
						Number:   proto.Int32(2),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						TypeName: strPtr(".config.v1.TraderConfig"),
					},
					{
						// A message field whose target has NO config
						// annotations must NOT become a block reference.
						Name:     strPtr("unrelated"),
						Number:   proto.Int32(3),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						TypeName: strPtr(".config.v1.NotConfig"),
					},
				},
			},
			{
				Name: strPtr("NotConfig"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   strPtr("whatever"),
						Number: proto.Int32(1),
						Type:   descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						Label:  descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					},
				},
			},
		},
	}

	gf := buildConfigPlugin(t, file)

	var configs []codegen.ConfigMessage
	for _, msg := range gf.Messages {
		appendConfigMessages(&configs, msg)
	}

	byName := map[string]codegen.ConfigMessage{}
	for _, cm := range configs {
		byName[cm.Name] = cm
	}

	trader, ok := byName["TraderConfig"]
	if !ok {
		t.Fatalf("TraderConfig not extracted; got %+v", configs)
	}
	if len(trader.Fields) != 1 || trader.Fields[0].GoName != "MaxPerTick" || trader.Fields[0].EnvVar != "TRADER_MAX_PER_TICK" {
		t.Errorf("TraderConfig fields = %+v", trader.Fields)
	}

	app, ok := byName["AppConfig"]
	if !ok {
		t.Fatalf("AppConfig not extracted; got %+v", configs)
	}
	var blockRef *codegen.ConfigField
	for i := range app.Fields {
		if app.Fields[i].Name == "trader" {
			blockRef = &app.Fields[i]
		}
		if app.Fields[i].Name == "unrelated" {
			t.Errorf("message field referencing a non-config message must not be extracted: %+v", app.Fields[i])
		}
	}
	if blockRef == nil {
		t.Fatalf("AppConfig missing block reference field; fields = %+v", app.Fields)
	}
	if blockRef.ProtoType != "message" || blockRef.MessageType != "TraderConfig" || blockRef.GoName != "Trader" {
		t.Errorf("block reference = %+v, want ProtoType=message MessageType=TraderConfig GoName=Trader", blockRef)
	}
	if blockRef.EnvVar != "" {
		t.Errorf("block reference must carry no env_var of its own, got %q", blockRef.EnvVar)
	}
}

// TestExtractConfigMessage_NestedBlockDeclaration: a block message
// declared INSIDE AppConfig (nested) is still extracted as its own
// ConfigMessage so the config loader can emit the struct type.
func TestExtractConfigMessage_NestedBlockDeclaration(t *testing.T) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    strPtr("config/v1/config.proto"),
		Package: strPtr("config.v1"),
		Syntax:  strPtr("proto3"),
		Options: &descriptorpb.FileOptions{
			GoPackage: strPtr("example.com/proj/gen/config/v1;configv1"),
		},
		Dependency: []string{"forge/v1/forge.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: strPtr("AppConfig"),
				NestedType: []*descriptorpb.DescriptorProto{
					{
						Name: strPtr("TraderConfig"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name:    strPtr("max_per_tick"),
								Number:  proto.Int32(1),
								Type:    descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
								Label:   descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Options: configFieldOpts("TRADER_MAX_PER_TICK"),
							},
						},
					},
				},
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     strPtr("trader"),
						Number:   proto.Int32(1),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						TypeName: strPtr(".config.v1.AppConfig.TraderConfig"),
					},
				},
			},
		},
	}

	gf := buildConfigPlugin(t, file)

	var configs []codegen.ConfigMessage
	for _, msg := range gf.Messages {
		appendConfigMessages(&configs, msg)
	}

	names := map[string]bool{}
	for _, cm := range configs {
		names[cm.Name] = true
	}
	if !names["AppConfig"] || !names["TraderConfig"] {
		t.Fatalf("expected both AppConfig and nested TraderConfig extracted; got %+v", configs)
	}
}
