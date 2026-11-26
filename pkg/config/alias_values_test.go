package config

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// A value-mapped alias: reliant's DEBUG=true / RELIANT_DEV_DEBUG=true, which
// mean a debug log level without carrying one. The variables are prefixed so
// a developer's own DEBUG cannot leak in.
const (
	avLevel    = "AVTEST_LOG_LEVEL"
	avDebug    = "AVTEST_DEBUG"
	avDevDebug = "AVTEST_DEV_DEBUG"
)

func buildAliasValuesMessage(t *testing.T) protoreflect.Message {
	t.Helper()
	for _, k := range []string{avLevel, avDebug, avDevDebug} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("config_alias_values_test.proto"),
		Package: proto.String("config.aliasvaluestest.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("LogConfig"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("level"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("level"),
				Options: configOpt(&forgepb.ConfigFieldOptions{
					EnvVar: avLevel, Flag: "log-level", DefaultValue: "info", Description: "log level",
					EnvVarAliases: []*forgepb.EnvVarAlias{
						{Name: avDebug, Values: map[string]string{"true": "debug", "1": "debug"}},
						{Name: avDevDebug, Values: map[string]string{"true": "debug"}},
					},
				}),
			}},
		}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().Get(0))
}

func levelOf(t *testing.T, env map[string]string) string {
	t.Helper()
	m := buildAliasValuesMessage(t)
	for k, v := range env {
		t.Setenv(k, v)
	}
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto(%v): %v", env, err)
	}
	return m.Get(m.Descriptor().Fields().ByName("level")).String()
}

func TestAliasValues_MapTheConventionOntoTheField(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want string
	}{
		{nil, "info"},
		{map[string]string{avDebug: "true"}, "debug"},
		{map[string]string{avDebug: "1"}, "debug"},
		{map[string]string{avDevDebug: "true"}, "debug"},
		// Another tool's DEBUG is not ours to parse: ignored, not refused.
		{map[string]string{avDebug: "express:*"}, "info"},
		{map[string]string{avDebug: "TRUE"}, "info"}, // keys match exactly
		// An unhonoured value falls through to the next name.
		{map[string]string{avDebug: "express:*", avDevDebug: "true"}, "debug"},
		// The field's own variable wins.
		{map[string]string{avLevel: "warn", avDebug: "true"}, "warn"},
	} {
		if got := levelOf(t, c.env); got != c.want {
			t.Errorf("env %v: level = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestAliasValues_HelpNamesTheSettingsHonoured(t *testing.T) {
	m := buildAliasValuesMessage(t)
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	if err := RegisterFlagsFor(fs, m.Interface()); err != nil {
		t.Fatal(err)
	}
	if got := fs.Lookup("log-level").Usage; !strings.Contains(got, "[env: "+avLevel+", "+avDebug+"=1|true, "+avDevDebug+"=true]") {
		t.Errorf("usage = %q", got)
	}
}
