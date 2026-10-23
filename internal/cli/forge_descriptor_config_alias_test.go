package cli

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/reliant-labs/forge/internal/codegen"
	forgev1 "github.com/reliant-labs/forge/pkg/forgepb"
)

// TestExtractConfigMessage_EnvVarAliases: the descriptor carries a field's
// env_var_aliases in declared order, deprecation included, so generate can
// refuse a colliding alias (codegen.ValidateConfigEnvAliases). Without this
// the check would see no aliases and pass everything.
func TestExtractConfigMessage_EnvVarAliases(t *testing.T) {
	opts := &descriptorpb.FieldOptions{}
	proto.SetExtension(opts, forgev1.E_Config, &forgev1.ConfigFieldOptions{
		EnvVar: "RELIANT_CONTROL_PLANE_URL",
		EnvVarAliases: []*forgev1.EnvVarAlias{
			{Name: "CONTROL_PLANE_API_URL"},
			{Name: "CONTROL_PLANE_BASE_URL", Deprecated: true},
		},
	})
	file := &descriptorpb.FileDescriptorProto{
		Name:       strPtr("config/v1/config.proto"),
		Package:    strPtr("config.v1"),
		Syntax:     strPtr("proto3"),
		Options:    &descriptorpb.FileOptions{GoPackage: strPtr("example.com/proj/gen/config/v1;configv1")},
		Dependency: []string{"forge/v1/forge.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: strPtr("AppConfig"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{
					Name:    strPtr("url"),
					Number:  proto.Int32(1),
					Type:    descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					Label:   descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Options: opts,
				},
				{
					Name:    strPtr("port"),
					Number:  proto.Int32(2),
					Type:    descriptorpb.FieldDescriptorProto_TYPE_INT32.Enum(),
					Label:   descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Options: configFieldOpts("PORT"),
				},
			},
		}},
	}

	gf := buildConfigPlugin(t, file)
	var configs []codegen.ConfigMessage
	for _, msg := range gf.Messages {
		appendConfigMessages(&configs, msg)
	}
	if len(configs) != 1 || len(configs[0].Fields) != 2 {
		t.Fatalf("want AppConfig with two fields, got %+v", configs)
	}
	want := []codegen.EnvVarAlias{{Name: "CONTROL_PLANE_API_URL"}, {Name: "CONTROL_PLANE_BASE_URL", Deprecated: true}}
	if got := configs[0].Fields[0].EnvVarAliases; !reflect.DeepEqual(got, want) {
		t.Errorf("url aliases = %+v, want %+v", got, want)
	}
	if got := configs[0].Fields[1].EnvVarAliases; got != nil {
		t.Errorf("port declares no aliases, got %+v (it must stay nil so the descriptor JSON omits it)", got)
	}
}
