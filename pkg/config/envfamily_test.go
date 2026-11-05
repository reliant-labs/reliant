package config

import (
	"os"
	"strings"
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

// reliant's two families: the RELIANT_OAUTH_<ID>_CLIENT_{ID,SECRET} pairs an
// integration catalog keys, and RELIANT_FEATURE_<NAME> flags. Prefixed so a
// developer's shell cannot supply one.
const (
	famOAuth   = "FAMTEST_OAUTH_"
	famFeature = "FAMTEST_FEATURE_"
)

var famFile *protoreflect.FileDescriptor

func buildFamilyMessage(t *testing.T) protoreflect.Message {
	t.Helper()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "FAMTEST_") {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	if famFile != nil {
		return dynamicpb.NewMessage((*famFile).Messages().ByName("IntegrationsConfig"))
	}
	entry := func(name string, valueType descriptorpb.FieldDescriptorProto_Type) *descriptorpb.DescriptorProto {
		return &descriptorpb.DescriptorProto{
			Name:    proto.String(name),
			Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("key"), Number: proto.Int32(1), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), JsonName: proto.String("key")},
				{Name: proto.String("value"), Number: proto.Int32(2), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: valueType.Enum(), JsonName: proto.String("value")},
			},
		}
	}
	mapField := func(name string, num int32, entryName string, opt *forgepb.ConfigFieldOptions) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: proto.String(".config.famtest.v1.IntegrationsConfig." + entryName),
			JsonName: proto.String(name), Options: configOpt(opt),
		}
	}
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("config_family_test.proto"),
		Package: proto.String("config.famtest.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("IntegrationsConfig"),
			NestedType: []*descriptorpb.DescriptorProto{
				entry("ClientIdsEntry", descriptorpb.FieldDescriptorProto_TYPE_STRING),
				entry("ClientSecretsEntry", descriptorpb.FieldDescriptorProto_TYPE_STRING),
				entry("FeaturesEntry", descriptorpb.FieldDescriptorProto_TYPE_BOOL),
				entry("TiersEntry", descriptorpb.FieldDescriptorProto_TYPE_STRING),
			},
			Field: []*descriptorpb.FieldDescriptorProto{
				mapField("client_ids", 1, "ClientIdsEntry", &forgepb.ConfigFieldOptions{EnvPrefix: famOAuth, EnvSuffix: "_CLIENT_ID"}),
				mapField("client_secrets", 2, "ClientSecretsEntry", &forgepb.ConfigFieldOptions{EnvPrefix: famOAuth, EnvSuffix: "_CLIENT_SECRET", Sensitive: true}),
				mapField("features", 3, "FeaturesEntry", &forgepb.ConfigFieldOptions{EnvPrefix: famFeature}),
				mapField("tiers", 4, "TiersEntry", &forgepb.ConfigFieldOptions{EnvPrefix: "FAMTEST_TIER_", AllowedValues: []string{"free", "pro"}}),
			},
		}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	famFile = &fd
	return dynamicpb.NewMessage(fd.Messages().ByName("IntegrationsConfig"))
}

func mapOf(m protoreflect.Message, field string) map[string]string {
	out := map[string]string{}
	m.Get(m.Descriptor().Fields().ByName(protoreflect.Name(field))).Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		out[k.String()] = v.String()
		return true
	})
	return out
}

func TestEnvFamily_LoadsEveryEntry(t *testing.T) {
	m := buildFamilyMessage(t)
	t.Setenv(famOAuth+"GITHUB_CLIENT_ID", "gh-id")
	t.Setenv(famOAuth+"GITHUB_CLIENT_SECRET", "gh-secret")
	t.Setenv(famOAuth+"GOOGLE_DRIVE_CLIENT_ID", "gd-id")
	t.Setenv(famOAuth+"EMPTY_CLIENT_ID", "") // "" is unset
	t.Setenv(famFeature+"DARK_MODE", "true")
	t.Setenv(famFeature+"BETA", "false")
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatal(err)
	}
	if got := mapOf(m, "client_ids"); len(got) != 2 || got["GITHUB"] != "gh-id" || got["GOOGLE_DRIVE"] != "gd-id" {
		t.Errorf("client_ids = %v", got)
	}
	if got := mapOf(m, "client_secrets"); len(got) != 1 || got["GITHUB"] != "gh-secret" {
		t.Errorf("client_secrets = %v", got)
	}
	if got := mapOf(m, "features"); got["DARK_MODE"] != "true" || got["BETA"] != "false" {
		t.Errorf("features = %v", got)
	}
}

func TestEnvFamily_FailsLoud(t *testing.T) {
	for _, c := range []struct {
		name, env, value, want string
	}{
		{"misspelled suffix under an owned prefix", famOAuth + "GITHUB_CLIENT_SECERT", "x",
			"env FAMTEST_OAUTH_GITHUB_CLIENT_SECERT is under the config family prefix FAMTEST_OAUTH_, but matches none of its families (FAMTEST_OAUTH_<KEY>_CLIENT_ID, FAMTEST_OAUTH_<KEY>_CLIENT_SECRET)"},
		{"value not of the map's kind", famFeature + "DARK_MODE", "maybe",
			`invalid value "maybe" for config field features["DARK_MODE"] (from env FAMTEST_FEATURE_DARK_MODE)`},
		{"empty key", famOAuth + "_CLIENT_ID", "x", "FAMTEST_OAUTH__CLIENT_ID is under the config family prefix"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := buildFamilyMessage(t)
			t.Setenv(c.env, c.value)
			err := LoadInto(nil, m.Interface())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error containing %q, got: %v", c.want, err)
			}
		})
	}
}

func TestEnvFamily_AllowedValuesConstrainEveryValue(t *testing.T) {
	m := buildFamilyMessage(t)
	t.Setenv("FAMTEST_TIER_ACME", "enterprise")
	// Loading validates, so the load itself refuses the out-of-set value.
	if err := LoadInto(nil, m.Interface()); err == nil || !strings.Contains(err.Error(), `config field tiers: value "enterprise" is not one of the allowed values [free pro]`) {
		t.Fatalf("LoadInto = %v, want the out-of-set tier refused", err)
	}
	t.Setenv("FAMTEST_TIER_ACME", "pro")
	if err := LoadInto(nil, buildFamilyMessage(t).Interface()); err != nil {
		t.Fatalf("an in-set tier was refused: %v", err)
	}
}

// Another config the same process loads may claim a name under the prefix:
// the owned-prefix check sees every config recorded on the command path.
func TestEnvFamily_AVariableAnotherConfigOfTheProcessClaimsIsFine(t *testing.T) {
	m := buildFamilyMessage(t)
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("config_family_other_test.proto"),
		Package: proto.String("config.famothertest.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("OAuthServerConfig"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name: proto.String("issuer"), Number: proto.Int32(1),
				Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("issuer"), Options: configOpt(&forgepb.ConfigFieldOptions{EnvVar: famOAuth + "ISSUER"}),
			}},
		}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}
	other := dynamicpb.NewMessage(fd.Messages().Get(0))
	root := &cobra.Command{Use: "app"}
	sub := &cobra.Command{Use: "sub"}
	root.AddCommand(sub)
	recordFileMessage(root, other.Interface())
	recordFileMessage(sub, m.Interface())
	t.Setenv(famOAuth+"ISSUER", "https://issuer")
	if err := LoadInto(sub, m.Interface()); err != nil {
		t.Fatalf("a name another config of the process declares was refused: %v", err)
	}
	// Without that config on the path, the same variable is unclaimed.
	if err := LoadInto(nil, m.Interface()); err == nil {
		t.Fatal("an unclaimed variable under the prefix loaded")
	}
}
