package config

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// Loading IS validating. A config whose annotations reject its resolved
// values must never reach the process: every generated Load<Msg> went through
// LoadInto and then left the declared checks to a second call, Validate<Msg>,
// that a caller could forget, so an out-of-set value booted. These pin that
// LoadInto itself refuses each invariant Validate declares.

func TestLoadInto_RejectsValueOutsideAllowedValues(t *testing.T) {
	t.Setenv("LOG_FORMAT", "xml")
	m := buildValidatableMessage(t)
	err := LoadInto(nil, m.Interface())
	if err == nil {
		t.Fatal("LoadInto accepted LOG_FORMAT=xml, which allowed_values [json text] rejects")
	}
	if !strings.Contains(err.Error(), "log_format") || !strings.Contains(err.Error(), `"xml"`) {
		t.Errorf("error must name the field and the rejected value; got: %v", err)
	}
}

func TestLoadInto_AcceptsValueInAllowedValues(t *testing.T) {
	t.Setenv("LOG_FORMAT", "json")
	m := buildValidatableMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto(LOG_FORMAT=json) = %v, want nil", err)
	}
}

func TestLoadInto_RejectsHalfConfiguredTLS(t *testing.T) {
	t.Setenv("TLS_CERT", "/etc/tls/cert.pem")
	m := buildValidatableMessage(t)
	if err := LoadInto(nil, m.Interface()); err == nil {
		t.Fatal("LoadInto accepted a TLS cert with no key (role=TLS_CERT/TLS_KEY are both-or-neither)")
	}
}

func TestLoadInto_RejectsWildcardCORSWithCredentials(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "*")
	t.Setenv("CORS_CREDS", "true")
	m := buildValidatableMessage(t)
	if err := LoadInto(nil, m.Interface()); err == nil {
		t.Fatal("LoadInto accepted a wildcard CORS origin with credentials")
	}
}

// A block composed onto the root is validated by the same load: the
// allowed_values check recurses, so it cannot hide one level down.
func TestLoadInto_RejectsOutOfSetValueInComposedBlock(t *testing.T) {
	t.Setenv("LOG_FORMAT", "yaml")
	m := buildNestedValidatableMessage(t)
	if err := LoadInto(nil, m.Interface()); err == nil {
		t.Fatal("LoadInto accepted LOG_FORMAT=yaml inside a composed block")
	}
}

// buildNestedValidatableMessage builds AppConfig { LogConfig log } where the
// block's one leaf carries allowed_values.
func buildNestedValidatableMessage(t *testing.T) protoreflect.Message {
	t.Helper()
	fo := &descriptorpb.FieldOptions{}
	proto.SetExtension(fo, forgepb.E_Config, &forgepb.ConfigFieldOptions{
		EnvVar: "LOG_FORMAT", AllowedValues: []string{"json", "text"},
	})
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("config_loadvalidatetest.proto"),
		Package: proto.String("config.loadvalidatetest.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("AppConfig"),
				Field: []*descriptorpb.FieldDescriptorProto{{
					Name:     proto.String("log"),
					Number:   proto.Int32(1),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
					TypeName: proto.String(".config.loadvalidatetest.v1.LogConfig"),
					JsonName: proto.String("log"),
				}},
			},
			{
				Name: proto.String("LogConfig"),
				Field: []*descriptorpb.FieldDescriptorProto{{
					Name:     proto.String("format"),
					Number:   proto.Int32(1),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					JsonName: proto.String("format"),
					Options:  fo,
				}},
			},
		},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().ByName("AppConfig"))
}
