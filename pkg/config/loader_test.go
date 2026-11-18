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
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// configOpt builds a descriptorpb.FieldOptions carrying a (forge.v1.config)
// extension, so we exercise the real descriptor-walk + extension-read path
// the loader relies on (rather than hand-stuffing maps).
func configOpt(o *forgepb.ConfigFieldOptions) *descriptorpb.FieldOptions {
	fo := &descriptorpb.FieldOptions{}
	proto.SetExtension(fo, forgepb.E_Config, o)
	return fo
}

// buildTestMessage compiles a single synthetic proto message whose fields
// span the scalar kinds the loader supports plus a well-known Duration and
// a Required field, and returns a fresh dynamicpb.Message for it. Using a
// dynamic message means the test needs no generated Go types while still
// driving the exact protoreflect Set path LoadInto uses.
func buildTestMessage(t *testing.T) protoreflect.Message {
	t.Helper()

	field := func(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type, opt *forgepb.ConfigFieldOptions) *descriptorpb.FieldDescriptorProto {
		fdp := &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     typ.Enum(),
			JsonName: proto.String(name),
		}
		if opt != nil {
			fdp.Options = configOpt(opt)
		}
		return fdp
	}

	durField := &descriptorpb.FieldDescriptorProto{
		Name:     proto.String("timeout"),
		Number:   proto.Int32(7),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
		TypeName: proto.String(".google.protobuf.Duration"),
		JsonName: proto.String("timeout"),
		Options: configOpt(&forgepb.ConfigFieldOptions{
			EnvVar:       "TIMEOUT",
			Flag:         "timeout",
			DefaultValue: "30s",
			Description:  "request timeout (Go duration)",
		}),
	}

	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("config_loader_testcfg.proto"),
		Package:    proto.String("config.loadertest.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/duration.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("TestConfig"),
				Field: []*descriptorpb.FieldDescriptorProto{
					field("log_level", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, &forgepb.ConfigFieldOptions{
						EnvVar: "LOG_LEVEL", Flag: "log-level", DefaultValue: "info", Description: "log level",
					}),
					field("port", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32, &forgepb.ConfigFieldOptions{
						EnvVar: "PORT", Flag: "port", DefaultValue: "8080", Description: "port",
					}),
					field("max_bytes", 3, descriptorpb.FieldDescriptorProto_TYPE_INT64, &forgepb.ConfigFieldOptions{
						EnvVar: "MAX_BYTES", DefaultValue: "1048576", Description: "max bytes (env only, no flag)",
					}),
					field("enabled", 4, descriptorpb.FieldDescriptorProto_TYPE_BOOL, &forgepb.ConfigFieldOptions{
						EnvVar: "ENABLED", Flag: "enabled", DefaultValue: "false", Description: "enabled",
					}),
					field("ratio", 5, descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, &forgepb.ConfigFieldOptions{
						EnvVar: "RATIO", Flag: "ratio", DefaultValue: "1.5", Description: "ratio",
					}),
					field("api_key", 6, descriptorpb.FieldDescriptorProto_TYPE_STRING, &forgepb.ConfigFieldOptions{
						EnvVar: "API_KEY", Required: true, Sensitive: true, Description: "required secret",
					}),
					durField,
					// No (forge.v1.config) annotation at all: must be skipped.
					field("ignored", 8, descriptorpb.FieldDescriptorProto_TYPE_STRING, nil),
					// Annotation present but blank: also skipped (not config-bound).
					field("also_ignored", 9, descriptorpb.FieldDescriptorProto_TYPE_STRING, &forgepb.ConfigFieldOptions{}),
				},
			},
		},
	}

	// Resolve the google.protobuf.Duration dependency from the global
	// registry (durationpb is linked in via the import above).
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	md := fd.Messages().Get(0)
	return dynamicpb.NewMessage(md)
}

// fieldByName is a small helper to read a set scalar back off the message.
func strOf(m protoreflect.Message, name string) string {
	return m.Get(m.Descriptor().Fields().ByName(protoreflect.Name(name))).String()
}
func intOf(m protoreflect.Message, name string) int64 {
	return m.Get(m.Descriptor().Fields().ByName(protoreflect.Name(name))).Int()
}
func boolOf(m protoreflect.Message, name string) bool {
	return m.Get(m.Descriptor().Fields().ByName(protoreflect.Name(name))).Bool()
}
func floatOf(m protoreflect.Message, name string) float64 {
	return m.Get(m.Descriptor().Fields().ByName(protoreflect.Name(name))).Float()
}
func durOf(t *testing.T, m protoreflect.Message, name string) *durationpb.Duration {
	t.Helper()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	v := m.Get(fd).Message().Interface()
	d, ok := v.(*durationpb.Duration)
	if !ok {
		// dynamicpb returns a dynamic message; round-trip through proto to a durationpb.
		d = &durationpb.Duration{}
		proto.Merge(d, v)
	}
	return d
}

// newCmd returns a cobra command with the message's flags registered, so
// tests can exercise the flag-changed branch of the precedence.
func newCmd(t *testing.T, msg proto.Message) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
	if err := RegisterFlagsFor(cmd.Flags(), msg); err != nil {
		t.Fatalf("RegisterFlagsFor: %v", err)
	}
	return cmd
}

func TestLoadInto_Defaults(t *testing.T) {
	t.Setenv("API_KEY", "sekret") // satisfy the required field
	m := buildTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := strOf(m, "log_level"); got != "info" {
		t.Errorf("log_level default = %q, want info", got)
	}
	if got := intOf(m, "port"); got != 8080 {
		t.Errorf("port default = %d, want 8080", got)
	}
	if got := intOf(m, "max_bytes"); got != 1048576 {
		t.Errorf("max_bytes default = %d, want 1048576", got)
	}
	if got := boolOf(m, "enabled"); got != false {
		t.Errorf("enabled default = %v, want false", got)
	}
	if got := floatOf(m, "ratio"); got != 1.5 {
		t.Errorf("ratio default = %v, want 1.5", got)
	}
	if d := durOf(t, m, "timeout"); d.AsDuration().String() != "30s" {
		t.Errorf("timeout default = %v, want 30s", d.AsDuration())
	}
}

func TestLoadInto_EnvOverridesDefault(t *testing.T) {
	t.Setenv("API_KEY", "sekret")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("PORT", "9090")
	t.Setenv("ENABLED", "true")
	t.Setenv("TIMEOUT", "5m")
	m := buildTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := strOf(m, "log_level"); got != "debug" {
		t.Errorf("log_level = %q, want debug (env)", got)
	}
	if got := intOf(m, "port"); got != 9090 {
		t.Errorf("port = %d, want 9090 (env)", got)
	}
	if got := boolOf(m, "enabled"); got != true {
		t.Errorf("enabled = %v, want true (env)", got)
	}
	if d := durOf(t, m, "timeout"); d.AsDuration().String() != "5m0s" {
		t.Errorf("timeout = %v, want 5m (env)", d.AsDuration())
	}
}

func TestLoadInto_FlagOverridesEnv(t *testing.T) {
	t.Setenv("API_KEY", "sekret")
	t.Setenv("LOG_LEVEL", "debug") // env says debug
	t.Setenv("PORT", "9090")       // env says 9090
	m := buildTestMessage(t)
	cmd := newCmd(t, m.Interface())
	// flag says warn / 7000 — flag must win over env.
	if err := cmd.Flags().Set("log-level", "warn"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("port", "7000"); err != nil {
		t.Fatal(err)
	}
	if err := LoadInto(cmd, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := strOf(m, "log_level"); got != "warn" {
		t.Errorf("log_level = %q, want warn (flag beats env)", got)
	}
	if got := intOf(m, "port"); got != 7000 {
		t.Errorf("port = %d, want 7000 (flag beats env)", got)
	}
}

func TestLoadInto_UnchangedFlagDefersToEnv(t *testing.T) {
	// A registered-but-unchanged flag must NOT win: precedence falls
	// through to env. This pins the cmd.Flags().Changed gate.
	t.Setenv("API_KEY", "sekret")
	t.Setenv("PORT", "9090")
	m := buildTestMessage(t)
	cmd := newCmd(t, m.Interface()) // flags registered, none Set()
	if err := LoadInto(cmd, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := intOf(m, "port"); got != 9090 {
		t.Errorf("port = %d, want 9090 (env, flag unchanged)", got)
	}
}

func TestLoadInto_RequiredMissingErrors(t *testing.T) {
	// API_KEY is required with no default and no env → error.
	m := buildTestMessage(t)
	err := LoadInto(nil, m.Interface())
	if err == nil {
		t.Fatal("LoadInto: want error for missing required field, got nil")
	}
	if want := "required config field api_key is not set"; !contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), want)
	}
}

func TestLoadInto_RequiredSatisfiedByEnv(t *testing.T) {
	t.Setenv("API_KEY", "live-value")
	m := buildTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := strOf(m, "api_key"); got != "live-value" {
		t.Errorf("api_key = %q, want live-value", got)
	}
}

func TestLoadInto_MalformedValueErrors(t *testing.T) {
	t.Setenv("API_KEY", "sekret")
	t.Setenv("PORT", "not-a-number")
	m := buildTestMessage(t)
	err := LoadInto(nil, m.Interface())
	if err == nil {
		t.Fatal("LoadInto: want error for malformed int, got nil")
	}
	if !contains(err.Error(), "PORT") {
		t.Errorf("error = %q, want it to mention the env var PORT", err.Error())
	}
}

func TestLoadInto_MalformedDurationErrors(t *testing.T) {
	t.Setenv("API_KEY", "sekret")
	t.Setenv("TIMEOUT", "5 furlongs")
	m := buildTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err == nil {
		t.Fatal("LoadInto: want error for malformed duration, got nil")
	}
}

func TestRegisterFlags_RegistersOnlyFlaggedFields(t *testing.T) {
	m := buildTestMessage(t)
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	if err := RegisterFlagsFor(fs, m.Interface()); err != nil {
		t.Fatalf("RegisterFlagsFor: %v", err)
	}

	// Flagged fields are present with proto defaults and correct types.
	cases := []struct{ name, def string }{
		{"log-level", "info"},
		{"port", "8080"},
		{"enabled", "false"},
		{"ratio", "1.5"},
		{"timeout", "30s"},
	}
	for _, c := range cases {
		f := fs.Lookup(c.name)
		if f == nil {
			t.Errorf("flag %q not registered", c.name)
			continue
		}
		if f.DefValue != c.def {
			t.Errorf("flag %q default = %q, want %q", c.name, f.DefValue, c.def)
		}
	}

	// max_bytes has env_var but no flag → no flag registered (mirrors the
	// generated RegisterFlags skipping flag-less fields).
	if f := fs.Lookup("max_bytes"); f != nil {
		t.Errorf("max_bytes should have no flag (env-only), got %v", f)
	}
	// api_key (required, no flag) is likewise flag-less.
	if f := fs.Lookup("api_key"); f != nil {
		t.Errorf("api_key should have no flag, got %v", f)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
