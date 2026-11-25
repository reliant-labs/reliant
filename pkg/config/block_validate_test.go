package config

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// buildBlockMessage compiles a synthetic root config message that composes a
// nested config block (TraderConfig) as a singular message field, exercising
// the block-recursion path of RegisterFlags/Load. The block leaf carries its
// own env/flag annotation; the root carries one scalar.
func buildBlockMessage(t *testing.T) protoreflect.Message {
	t.Helper()

	rootField := func(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type, opt *forgepb.ConfigFieldOptions) *descriptorpb.FieldDescriptorProto {
		fdp := &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     typ.Enum(),
			JsonName: proto.String(name),
		}
		if opt != nil {
			fo := &descriptorpb.FieldOptions{}
			proto.SetExtension(fo, forgepb.E_Config, opt)
			fdp.Options = fo
		}
		return fdp
	}

	blockMsg := &descriptorpb.DescriptorProto{
		Name: proto.String("TraderConfig"),
		Field: []*descriptorpb.FieldDescriptorProto{
			rootField("max_per_tick", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32, &forgepb.ConfigFieldOptions{
				EnvVar: "TRADER_MAX_PER_TICK", Flag: "trader-max-per-tick", DefaultValue: "10", Description: "max per tick",
			}),
		},
	}

	blockFieldDesc := &descriptorpb.FieldDescriptorProto{
		Name:     proto.String("trader"),
		Number:   proto.Int32(2),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
		TypeName: proto.String(".config.blocktest.v1.TraderConfig"),
		JsonName: proto.String("trader"),
	}

	rootMsg := &descriptorpb.DescriptorProto{
		Name: proto.String("AppConfig"),
		Field: []*descriptorpb.FieldDescriptorProto{
			rootField("port", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32, &forgepb.ConfigFieldOptions{
				EnvVar: "PORT", Flag: "port", DefaultValue: "8080", Description: "port",
			}),
			blockFieldDesc,
		},
	}

	fdp := &descriptorpb.FileDescriptorProto{
		Name:        proto.String("config_blocktest.proto"),
		Package:     proto.String("config.blocktest.v1"),
		Syntax:      proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{rootMsg, blockMsg},
	}

	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().ByName("AppConfig"))
}

func blockIntOf(t *testing.T, m protoreflect.Message, block, leaf string) int64 {
	t.Helper()
	bf := m.Descriptor().Fields().ByName(protoreflect.Name(block))
	sub := m.Get(bf).Message()
	return sub.Get(sub.Descriptor().Fields().ByName(protoreflect.Name(leaf))).Int()
}

func TestBlockRecursion_RegisterFlags(t *testing.T) {
	m := buildBlockMessage(t)
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	if err := RegisterFlagsFor(fs, m.Interface()); err != nil {
		t.Fatalf("RegisterFlagsFor: %v", err)
	}
	// Root flag and the nested-block leaf flag are both registered (flat
	// namespace, leaf keeps its own annotation).
	if f := fs.Lookup("port"); f == nil || f.DefValue != "8080" {
		t.Errorf("port flag = %v, want default 8080", f)
	}
	if f := fs.Lookup("trader-max-per-tick"); f == nil || f.DefValue != "10" {
		t.Errorf("trader-max-per-tick flag = %v, want default 10", f)
	}
}

func TestBlockRecursion_LoadDefaults(t *testing.T) {
	m := buildBlockMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := blockIntOf(t, m, "trader", "max_per_tick"); got != 10 {
		t.Errorf("trader.max_per_tick default = %d, want 10", got)
	}
}

func TestBlockRecursion_LoadEnvAndFlag(t *testing.T) {
	t.Setenv("TRADER_MAX_PER_TICK", "50")
	m := buildBlockMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := blockIntOf(t, m, "trader", "max_per_tick"); got != 50 {
		t.Errorf("trader.max_per_tick env = %d, want 50", got)
	}

	// Flag beats env for the nested leaf too.
	m2 := buildBlockMessage(t)
	cmd := &cobra.Command{Use: "t"}
	if err := RegisterFlagsFor(cmd.Flags(), m2.Interface()); err != nil {
		t.Fatalf("RegisterFlagsFor: %v", err)
	}
	if err := cmd.Flags().Set("trader-max-per-tick", "99"); err != nil {
		t.Fatal(err)
	}
	if err := LoadInto(cmd, m2.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := blockIntOf(t, m2, "trader", "max_per_tick"); got != 99 {
		t.Errorf("trader.max_per_tick flag = %d, want 99 (flag beats env)", got)
	}
}

// buildValidatableMessage builds a message carrying the validator-role
// annotations: a log_format with allowed_values, a TLS keypair, and CORS
// origins + credentials. The caller seeds the values via setStr/setBool.
func buildValidatableMessage(t *testing.T) protoreflect.Message {
	t.Helper()

	field := func(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type, opt *forgepb.ConfigFieldOptions) *descriptorpb.FieldDescriptorProto {
		fdp := &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     typ.Enum(),
			JsonName: proto.String(name),
		}
		fo := &descriptorpb.FieldOptions{}
		proto.SetExtension(fo, forgepb.E_Config, opt)
		fdp.Options = fo
		return fdp
	}

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("config_validatetest.proto"),
		Package: proto.String("config.validatetest.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("AppConfig"),
				Field: []*descriptorpb.FieldDescriptorProto{
					field("log_format", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, &forgepb.ConfigFieldOptions{
						EnvVar: "LOG_FORMAT", AllowedValues: []string{"json", "text"},
					}),
					field("tls_cert", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING, &forgepb.ConfigFieldOptions{
						EnvVar: "TLS_CERT", Role: forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_TLS_CERT,
					}),
					field("tls_key", 3, descriptorpb.FieldDescriptorProto_TYPE_STRING, &forgepb.ConfigFieldOptions{
						EnvVar: "TLS_KEY", Role: forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_TLS_KEY,
					}),
					field("cors_origins", 4, descriptorpb.FieldDescriptorProto_TYPE_STRING, &forgepb.ConfigFieldOptions{
						EnvVar: "CORS_ORIGINS", Role: forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_CORS_ORIGINS,
					}),
					field("cors_creds", 5, descriptorpb.FieldDescriptorProto_TYPE_BOOL, &forgepb.ConfigFieldOptions{
						EnvVar: "CORS_CREDS", Role: forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_CORS_ALLOW_CREDENTIALS,
					}),
				},
			},
		},
	}

	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().Get(0))
}

func setBoolField(m protoreflect.Message, name string, v bool) {
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	m.Set(fd, protoreflect.ValueOfBool(v))
}

func TestValidate_AllowedValues(t *testing.T) {
	m := buildValidatableMessage(t)
	setStr(t, m, "log_format", "json")
	if err := Validate(m.Interface()); err != nil {
		t.Errorf("Validate(json) = %v, want nil", err)
	}
	setStr(t, m, "log_format", "xml")
	if err := Validate(m.Interface()); err == nil {
		t.Error("Validate(xml) = nil, want error for value outside allowed set")
	}
	// Empty is always allowed (unset field).
	setStr(t, m, "log_format", "")
	if err := Validate(m.Interface()); err != nil {
		t.Errorf("Validate(empty) = %v, want nil", err)
	}
}

func TestValidate_TLSPair(t *testing.T) {
	m := buildValidatableMessage(t)
	// Neither set: ok.
	if err := Validate(m.Interface()); err != nil {
		t.Errorf("Validate(no tls) = %v, want nil", err)
	}
	// Both set: ok.
	setStr(t, m, "tls_cert", "/c")
	setStr(t, m, "tls_key", "/k")
	if err := Validate(m.Interface()); err != nil {
		t.Errorf("Validate(both tls) = %v, want nil", err)
	}
	// Only cert: error.
	setStr(t, m, "tls_key", "")
	if err := Validate(m.Interface()); err == nil {
		t.Error("Validate(cert only) = nil, want both-or-neither error")
	}
}

func TestValidate_CORSWildcardWithCreds(t *testing.T) {
	m := buildValidatableMessage(t)
	setStr(t, m, "cors_origins", "*")
	setBoolField(m, "cors_creds", true)
	if err := Validate(m.Interface()); err == nil {
		t.Error("Validate(* + creds) = nil, want spec-invalid error")
	}
	// Wildcard without creds: ok.
	setBoolField(m, "cors_creds", false)
	if err := Validate(m.Interface()); err != nil {
		t.Errorf("Validate(* no creds) = %v, want nil", err)
	}
	// Explicit origin + creds: ok.
	setStr(t, m, "cors_origins", "https://app.example.com")
	setBoolField(m, "cors_creds", true)
	if err := Validate(m.Interface()); err != nil {
		t.Errorf("Validate(explicit + creds) = %v, want nil", err)
	}
}
