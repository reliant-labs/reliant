package config

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// buildReachMessage builds AppConfig { string database_url (required);
// TickConfig tick { string token (required) } }.
func buildReachMessage(t *testing.T) protoreflect.Message {
	t.Helper()
	opt := func(env string) *descriptorpb.FieldOptions {
		fo := &descriptorpb.FieldOptions{}
		proto.SetExtension(fo, forgepb.E_Config, &forgepb.ConfigFieldOptions{EnvVar: env, Required: true})
		return fo
	}
	str := func(name, env string) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(1), JsonName: proto.String(name),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:  descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(), Options: opt(env),
		}
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name: proto.String("config_requiredwithin.proto"), Package: proto.String("config.requiredwithin.v1"),
		Syntax: proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("AppConfig"), Field: []*descriptorpb.FieldDescriptorProto{
				str("database_url", "RW_DATABASE_URL"),
				{
					Name: proto.String("tick"), Number: proto.Int32(2), JsonName: proto.String("tick"),
					Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
					TypeName: proto.String(".config.requiredwithin.v1.TickConfig"),
				},
			}},
			{Name: proto.String("TickConfig"), Field: []*descriptorpb.FieldDescriptorProto{str("token", "RW_TICK_TOKEN")}},
		},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().ByName("AppConfig"))
}

func TestLoadInto_RequiredWithinNarrowsTheDemandToTheReachedFields(t *testing.T) {
	// No option: both required fields are demanded.
	if err := LoadInto(nil, buildReachMessage(t).Interface()); err == nil || !contains(err.Error(), "database_url") {
		t.Fatalf("an unscoped load must demand every required field: %v", err)
	}
	// A root reaching only the tick block: the database is not demanded,
	// the block's leaf (by path) still is.
	err := LoadInto(nil, buildReachMessage(t).Interface(), RequiredWithin("tick.token"))
	if err == nil || !contains(err.Error(), "required config field token") {
		t.Fatalf("a reached block leaf stays required: %v", err)
	}
	t.Setenv("RW_TICK_TOKEN", "t")
	m := buildReachMessage(t)
	if err := LoadInto(nil, m.Interface(), RequiredWithin("tick.token")); err != nil {
		t.Fatalf("an unreached required field must not fail the load: %v", err)
	}
	// Every field still loads: an unreached field set in the env is read.
	t.Setenv("RW_DATABASE_URL", "postgres://x")
	m = buildReachMessage(t)
	if err := LoadInto(nil, m.Interface(), RequiredWithin("tick.token")); err != nil {
		t.Fatal(err)
	}
	if got := strOf(m, "database_url"); got != "postgres://x" {
		t.Errorf("database_url = %q: an unreached field still loads", got)
	}
}
