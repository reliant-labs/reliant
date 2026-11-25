package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// everyKindField is one field of the every-kind fixture: a proto shape the
// env layer accepts, with a default no zero value could be mistaken for.
type everyKindField struct {
	name     string
	typ      descriptorpb.FieldDescriptorProto_Type
	typeName string // message type, for a Duration
	repeated bool
	def      string
}

// everyKindFields spans every field shape the env layer can set: each
// scalar kind, a Duration, and repeated fields of two element kinds.
var everyKindFields = []everyKindField{
	{name: "s", typ: descriptorpb.FieldDescriptorProto_TYPE_STRING, def: "localhost"},
	{name: "b", typ: descriptorpb.FieldDescriptorProto_TYPE_BYTES, def: "raw"},
	{name: "i32", typ: descriptorpb.FieldDescriptorProto_TYPE_INT32, def: "-7"},
	{name: "si32", typ: descriptorpb.FieldDescriptorProto_TYPE_SINT32, def: "-8"},
	{name: "sf32", typ: descriptorpb.FieldDescriptorProto_TYPE_SFIXED32, def: "-9"},
	{name: "i64", typ: descriptorpb.FieldDescriptorProto_TYPE_INT64, def: "70"},
	{name: "si64", typ: descriptorpb.FieldDescriptorProto_TYPE_SINT64, def: "-80"},
	{name: "sf64", typ: descriptorpb.FieldDescriptorProto_TYPE_SFIXED64, def: "90"},
	{name: "u32", typ: descriptorpb.FieldDescriptorProto_TYPE_UINT32, def: "7"},
	{name: "f32", typ: descriptorpb.FieldDescriptorProto_TYPE_FIXED32, def: "8"},
	{name: "u64", typ: descriptorpb.FieldDescriptorProto_TYPE_UINT64, def: "9"},
	{name: "f64", typ: descriptorpb.FieldDescriptorProto_TYPE_FIXED64, def: "10"},
	{name: "on", typ: descriptorpb.FieldDescriptorProto_TYPE_BOOL, def: "true"},
	{name: "fl", typ: descriptorpb.FieldDescriptorProto_TYPE_FLOAT, def: "0.5"},
	{name: "db", typ: descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, def: "1.5"},
	{name: "dur", typ: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, typeName: ".google.protobuf.Duration", def: "30s"},
	{name: "hosts", typ: descriptorpb.FieldDescriptorProto_TYPE_STRING, repeated: true, def: "a.example,b.example"},
	{name: "ports", typ: descriptorpb.FieldDescriptorProto_TYPE_INT32, repeated: true, def: "80,443"},
}

// envVarOf names the env var the every-kind fixture binds a field to.
func envVarOf(name string) string { return "EVERYKIND_" + strings.ToUpper(name) }

// buildEveryKindMessage compiles the every-kind fixture. Every field has an
// env var, its default and — except bytes, which RegisterFlagsFor cannot
// register — a flag of the same name.
func buildEveryKindMessage(t *testing.T) protoreflect.Message {
	t.Helper()
	var fields []*descriptorpb.FieldDescriptorProto
	for i, f := range everyKindFields {
		label := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
		if f.repeated {
			label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
		}
		flag := f.name
		if f.typ == descriptorpb.FieldDescriptorProto_TYPE_BYTES {
			flag = ""
		}
		fdp := &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(f.name),
			Number:   proto.Int32(int32(i + 1)),
			Label:    label.Enum(),
			Type:     f.typ.Enum(),
			JsonName: proto.String(f.name),
			Options: configOpt(&forgepb.ConfigFieldOptions{
				EnvVar: envVarOf(f.name), Flag: flag, DefaultValue: f.def,
			}),
		}
		if f.typeName != "" {
			fdp.TypeName = proto.String(f.typeName)
		}
		fields = append(fields, fdp)
	}
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:        proto.String("config_everykind_testcfg.proto"),
		Package:     proto.String("config.everykind.v1"),
		Syntax:      proto.String("proto3"),
		Dependency:  []string{"google/protobuf/duration.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("EveryKind"), Field: fields}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().Get(0))
}

// unsetEveryKindEnv leaves every fixture variable absent for the test.
func unsetEveryKindEnv(t *testing.T) {
	t.Helper()
	for _, f := range everyKindFields {
		t.Setenv(envVarOf(f.name), "") // registers the restore
		if err := os.Unsetenv(envVarOf(f.name)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLoadInto_EmptyEnvIsUnset pins the env layer's empty-string rule for
// every field kind: a variable set to "" loads exactly what an absent one
// does. It used to be a value for strings, bytes and lists (TEMPORAL_HOST=""
// replaced the "localhost" default with "", ALLOWED_REGISTRIES="" cleared the
// default list) and unset for every other kind.
func TestLoadInto_EmptyEnvIsUnset(t *testing.T) {
	unsetEveryKindEnv(t)
	absent := buildEveryKindMessage(t)
	if err := LoadInto(nil, absent.Interface()); err != nil {
		t.Fatalf("LoadInto with no env: %v", err)
	}

	for _, f := range everyKindFields {
		t.Setenv(envVarOf(f.name), "")
	}
	empty := absent.New() // the same descriptor, so proto.Equal can compare them
	if err := LoadInto(nil, empty.Interface()); err != nil {
		t.Fatalf("LoadInto with every var set to \"\": %v — an empty var must never reach a parser", err)
	}

	if !proto.Equal(absent.Interface(), empty.Interface()) {
		t.Fatalf("an empty env var loaded differently from an absent one:\n  absent: %v\n  empty:  %v",
			protojson.Format(absent.Interface()), protojson.Format(empty.Interface()))
	}
	// And what both loaded is the defaults, not the zero values.
	if got := strOf(empty, "s"); got != "localhost" {
		t.Errorf("s = %q, want the default localhost", got)
	}
	if got := strListOf(t, empty, "hosts"); !eqStrs(got, []string{"a.example", "b.example"}) {
		t.Errorf("hosts = %q, want the default list", got)
	}
	if !boolOf(empty, "on") {
		t.Error("on = false, want the default true")
	}
}

// TestLoadInto_EmptyEnvKeepsTheFileValue: the layer an empty var falls
// through to is whatever came before it, not only the default. A config file
// that sets a value keeps it against an empty variable.
func TestLoadInto_EmptyEnvKeepsTheFileValue(t *testing.T) {
	unsetEveryKindEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("s: from-file\ni32: 12\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := buildEveryKindMessage(t)
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String(ConfigFlag, "", "")
	if err := cmd.Flags().Set(ConfigFlag, path); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envVarOf("s"), "")
	t.Setenv(envVarOf("i32"), "")
	if err := LoadInto(cmd, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := strOf(m, "s"); got != "from-file" {
		t.Errorf("s = %q, want the file's from-file", got)
	}
	if got := intOf(m, "i32"); got != 12 {
		t.Errorf("i32 = %d, want the file's 12", got)
	}
}

// TestLoadInto_EmptyFlagIsAValue: the flag is where an explicitly empty value
// lives now. `--s=` empties a defaulted string and `--hosts=` clears a
// defaulted list, over whatever the env said.
func TestLoadInto_EmptyFlagIsAValue(t *testing.T) {
	unsetEveryKindEnv(t)
	t.Setenv(envVarOf("s"), "from-env")
	m := buildEveryKindMessage(t)
	cmd := newCmd(t, m.Interface())
	for _, name := range []string{"s", "hosts"} {
		if err := cmd.Flags().Set(name, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := LoadInto(cmd, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := strOf(m, "s"); got != "" {
		t.Errorf(`s = %q, want "" (an empty flag is a value)`, got)
	}
	if got := strListOf(t, m, "hosts"); len(got) != 0 {
		t.Errorf("hosts = %q, want [] (an empty flag clears a defaulted list)", got)
	}
}

// TestLoadInto_RequiredSetButEmptyNamesIt: a required field whose variable is
// present but empty is still missing, and the error says the variable was
// found empty, so nobody goes looking for a variable that is in fact set.
func TestLoadInto_RequiredSetButEmptyNamesIt(t *testing.T) {
	t.Setenv("API_KEY", "")
	err := LoadInto(nil, buildTestMessage(t).Interface())
	if err == nil {
		t.Fatal("LoadInto: want the required-field error, got nil")
	}
	for _, want := range []string{"required config field api_key is not set", "API_KEY is set but empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}

	if err := os.Unsetenv("API_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := LoadInto(nil, buildTestMessage(t).Interface()); err == nil || strings.Contains(err.Error(), "set but empty") {
		t.Errorf("an absent variable must not be reported as set but empty: %v", err)
	}
}
