package config

import (
	"os"
	"path/filepath"
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

// The runtime presence contract of a sensitive field, and the documentation
// that states it.
//
// forge.proto used to say a sensitive field is required at runtime unless it
// is `optional: true`, while the loader required only `required: true`
// fields. The loader is right and the documentation was wrong: whether a
// credential must exist is decided per WORKLOAD (each projects only the
// secrets its config_secrets names, and the deploy pre-flight checks those),
// so a check implied by `sensitive` would refuse every workload that shares a
// config but deliberately reads none of some secret. These tests pin the
// behaviour and the sentence that describes it, so the two cannot drift
// apart again.

func buildSensitiveMessage(t *testing.T) protoreflect.Message {
	t.Helper()
	field := func(name string, num int32, opt *forgepb.ConfigFieldOptions) *descriptorpb.FieldDescriptorProto {
		fo := &descriptorpb.FieldOptions{}
		proto.SetExtension(fo, forgepb.E_Config, opt)
		return &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: proto.String(name),
			Options:  fo,
		}
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("config_sensitivetest.proto"),
		Package: proto.String("config.sensitivetest.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("AppConfig"),
			Field: []*descriptorpb.FieldDescriptorProto{
				// A key source the process uses only when another is unset.
				field("api_key", 1, &forgepb.ConfigFieldOptions{EnvVar: "API_KEY", Flag: "api-key", Sensitive: true}),
				// A credential the process cannot run without.
				field("db_password", 2, &forgepb.ConfigFieldOptions{EnvVar: "DB_PASSWORD", Flag: "db-password", Sensitive: true, Required: true}),
			},
		}},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().Get(0))
}

func TestLoadInto_SensitiveAloneDoesNotRequireAValue(t *testing.T) {
	t.Setenv("DB_PASSWORD", "s3cret")
	m := buildSensitiveMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto with API_KEY (sensitive, not required) unset = %v, want nil: "+
			"`sensitive` says where a value comes from, `required` whether there must be one", err)
	}
}

func TestLoadInto_RequiredSensitiveUnsetFailsNamingOnlyTheEnvVar(t *testing.T) {
	m := buildSensitiveMessage(t)
	err := LoadInto(nil, m.Interface())
	if err == nil {
		t.Fatal("LoadInto with DB_PASSWORD (sensitive, required) unset = nil, want a required-field error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "db_password") || !strings.Contains(msg, "DB_PASSWORD") {
		t.Errorf("error must name the field and its env var; got: %v", err)
	}
	if strings.Contains(msg, "--db-password") {
		t.Errorf("a sensitive field never takes a flag, so the error must not offer one; got: %v", err)
	}
}

// TestForgeProtoDocumentsSensitivePresenceAsTheLoaderDoes reads the
// ConfigFieldOptions documentation the loader above implements. The old
// text promised a runtime check the loader never made ("the runtime
// required-check skips it too", under a heading saying sensitive implies
// required), and an author who trusted it shipped a credential with no
// `required: true` and no check.
func TestForgeProtoDocumentsSensitivePresenceAsTheLoaderDoes(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "proto", "forge", "v1", "forge.proto"))
	if err != nil {
		t.Fatalf("read forge.proto: %v", err)
	}
	optionalDoc := commentAbove(t, string(src), "bool optional = 10;")
	for _, stale := range []string{"runtime required-check", "IMPLIES REQUIRED BY DEFAULT"} {
		if strings.Contains(optionalDoc, stale) {
			t.Errorf("ConfigFieldOptions.optional still documents a runtime check the loader does not make (%q)", stale)
		}
	}
	if !strings.Contains(optionalDoc, "DOES NOT IMPLY REQUIRED AT RUNTIME") {
		t.Error("ConfigFieldOptions.optional must say that sensitive does not imply required at runtime")
	}
	if requiredDoc := commentAbove(t, string(src), "bool required = 4;"); !strings.Contains(requiredDoc, "`sensitive` does not imply it") {
		t.Errorf("ConfigFieldOptions.required must say it is the one runtime presence check; got:\n%s", requiredDoc)
	}
}

// commentAbove returns the contiguous `//` comment block directly above the
// line containing decl.
func commentAbove(t *testing.T, src, decl string) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if !strings.Contains(l, decl) {
			continue
		}
		var block []string
		for j := i - 1; j >= 0; j-- {
			trimmed := strings.TrimSpace(lines[j])
			if !strings.HasPrefix(trimmed, "//") {
				break
			}
			block = append([]string{trimmed}, block...)
		}
		return strings.Join(block, "\n")
	}
	t.Fatalf("forge.proto declares no %q", decl)
	return ""
}
