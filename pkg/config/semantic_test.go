package config

import (
	"testing"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// buildModeMessage compiles a synthetic config message whose role=MODE field
// is named modeFieldName, so tests can prove behavior follows the ANNOTATION,
// not the field name. A second plain string field named distractorName lets a
// test assert that naming a field "environment" without the role does NOT make
// it the mode field.
func buildModeMessage(t *testing.T, modeFieldName, distractorName string) protoreflect.Message {
	t.Helper()

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("config_semantic_testcfg.proto"),
		Package: proto.String("config.semantictest.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("ModeConfig"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     proto.String(modeFieldName),
						Number:   proto.Int32(1),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						JsonName: proto.String(modeFieldName),
						Options: configOpt(&forgepb.ConfigFieldOptions{
							EnvVar:       "RUNTIME_ENV",
							Flag:         "runtime-env",
							DefaultValue: "production",
							Role:         forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_MODE,
							Description:  "runtime mode (role-tagged)",
						}),
					},
					{
						Name:     proto.String(distractorName),
						Number:   proto.Int32(2),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						JsonName: proto.String(distractorName),
						Options: configOpt(&forgepb.ConfigFieldOptions{
							EnvVar:       "DISTRACTOR",
							Flag:         "distractor",
							DefaultValue: "production",
							Description:  "NOT role-tagged — must never drive Mode",
						}),
					},
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

func setStr(t *testing.T, m protoreflect.Message, name, val string) {
	t.Helper()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		t.Fatalf("field %q not found", name)
	}
	m.Set(fd, protoreflect.ValueOfString(val))
}

// TestMode_FollowsAnnotationNotName is the core invariant: Mode reads the
// field tagged role=MODE regardless of what that field is NAMED. The mode
// field here is named "stage" (not "environment"), yet Mode tracks it.
func TestMode_FollowsAnnotationNotName(t *testing.T) {
	m := buildModeMessage(t, "stage", "environment")

	setStr(t, m, "stage", "development")
	if got := Mode(m.Interface()); got != ModeDevelopment {
		t.Errorf("Mode(stage=development) = %v, want ModeDevelopment (annotation drives it)", got)
	}
	setStr(t, m, "stage", "production")
	if got := Mode(m.Interface()); got != ModeProduction {
		t.Errorf("Mode(stage=production) = %v, want ModeProduction", got)
	}
}

// TestMode_RenameDoesNotChangeBehavior proves renaming the role field is a
// no-op for behavior: the same value yields the same Mode whether the field
// is named "stage" or "deployment_tier", because the annotation moves with it.
func TestMode_RenameDoesNotChangeBehavior(t *testing.T) {
	for _, name := range []string{"stage", "deployment_tier", "phase"} {
		m := buildModeMessage(t, name, "other")
		setStr(t, m, name, "dev")
		if got := Mode(m.Interface()); got != ModeDevelopment {
			t.Errorf("role field named %q: Mode = %v, want ModeDevelopment", name, got)
		}
	}
}

// TestMode_UnannotatedEnvironmentDoesNotTrigger pins the anti-magic: a field
// literally NAMED "environment" but WITHOUT the role annotation must never
// auto-enable dev mode. Only the role-tagged field (here "stage") counts.
func TestMode_UnannotatedEnvironmentDoesNotTrigger(t *testing.T) {
	m := buildModeMessage(t, "stage", "environment")
	// Set the unannotated "environment" field to development; the role field
	// stays production. Mode must report production.
	setStr(t, m, "environment", "development")
	setStr(t, m, "stage", "production")
	if got := Mode(m.Interface()); got != ModeProduction {
		t.Errorf("unannotated environment=development must NOT enable dev mode; got %v", got)
	}
}

// TestMode_NoRoleFieldIsProduction: a message with no role=MODE field always
// reports production (locked-down default).
func TestMode_NoRoleFieldIsProduction(t *testing.T) {
	m := buildTestMessage(t) // none of its fields carry a role
	if got := Mode(m.Interface()); got != ModeProduction {
		t.Errorf("Mode(no role field) = %v, want ModeProduction", got)
	}
}

// TestRegisterFlags_CobraAPI exercises the cobra-facing RegisterFlags/Load
// wrappers (the spec'd public names) end to end, and confirms a sensitive
// field gets NO flag.
func TestRegisterFlags_CobraAPI(t *testing.T) {
	t.Setenv("API_KEY", "sekret") // satisfy the required+sensitive field
	m := buildTestMessage(t)
	cmd := &cobra.Command{Use: "t", RunE: func(*cobra.Command, []string) error { return nil }}
	if err := RegisterFlags(cmd, m.Interface()); err != nil {
		t.Fatalf("RegisterFlags: %v", err)
	}
	if f := cmd.Flags().Lookup("log-level"); f == nil {
		t.Error("log-level flag should be registered")
	}
	// api_key is sensitive → no flag even though... (it also has no flag
	// annotation, but the sensitive guard is the belt-and-suspenders check).
	if f := cmd.Flags().Lookup("api-key"); f != nil {
		t.Error("sensitive field must not register a flag")
	}
	if err := cmd.Flags().Set("log-level", "warn"); err != nil {
		t.Fatal(err)
	}
	if err := Load(cmd, m.Interface()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := strOf(m, "log_level"); got != "warn" {
		t.Errorf("log_level = %q, want warn (flag)", got)
	}
}

// TestRegisterFlags_SensitiveSkipped builds a message with a sensitive field
// that DOES carry a flag annotation (a misconfiguration) and confirms the
// flag is still not registered.
func TestRegisterFlags_SensitiveSkipped(t *testing.T) {
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("config_sensitive_testcfg.proto"),
		Package: proto.String("config.sensitivetest.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("SecretConfig"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("token"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("token"),
				Options: configOpt(&forgepb.ConfigFieldOptions{
					EnvVar: "TOKEN", Flag: "token", Sensitive: true, Description: "secret with a (wrong) flag",
				}),
			}},
		}},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	m := dynamicpb.NewMessage(fd.Messages().Get(0))

	cmd := &cobra.Command{Use: "t"}
	if err := RegisterFlags(cmd, m.Interface()); err != nil {
		t.Fatalf("RegisterFlags: %v", err)
	}
	if f := cmd.Flags().Lookup("token"); f != nil {
		t.Error("sensitive field with a flag annotation must STILL be skipped")
	}

	// And it loads from env only, never the (absent) default.
	t.Setenv("TOKEN", "live-secret")
	if err := Load(cmd, m.Interface()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := strOf(m, "token"); got != "live-secret" {
		t.Errorf("token = %q, want live-secret (env)", got)
	}
}
