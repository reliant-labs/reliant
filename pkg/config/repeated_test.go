package config

import (
	"testing"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	_ "google.golang.org/protobuf/types/known/durationpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// Repeated (list) and map config fields.
//
// Regression cover for the loader panicking on any repeated field: parseValue
// switched on fd.Kind() alone, and a `repeated string` still reports
// StringKind, so it returned a scalar string and m.Set panicked with
//
//	type mismatch: cannot convert string to list
//
// as soon as the field's env var was present. The panic named neither the
// field nor its cardinality, so `repeated string` was unusable as a config
// field in any forge project and the failure gave no clue why.

// buildListTestMessage compiles a synthetic config message covering every
// cardinality the loader has to survive: repeated string/int32 (supported),
// repeated google.protobuf.Duration (supported — the list check has to come
// BEFORE the duration branch), a map (rejected), and a repeated non-duration
// message (rejected). The rejections must be diagnostics that name the field,
// never panics.
func buildListTestMessage(t *testing.T) protoreflect.Message {
	t.Helper()

	listField := func(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type, opt *forgepb.ConfigFieldOptions) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
			Type:     typ.Enum(),
			JsonName: proto.String(name),
			Options:  configOpt(opt),
		}
	}
	msgListField := func(name string, num int32, typeName string, opt *forgepb.ConfigFieldOptions) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
			TypeName: proto.String(typeName),
			JsonName: proto.String(name),
			Options:  configOpt(opt),
		}
	}

	const pkg = ".config.repeatedtest.v1."

	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("config_repeated_testcfg.proto"),
		Package:    proto.String("config.repeatedtest.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/duration.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("ListConfig"),
				Field: []*descriptorpb.FieldDescriptorProto{
					listField("allowed_registries", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING,
						&forgepb.ConfigFieldOptions{
							EnvVar: "ALLOWED_REGISTRIES", Flag: "allowed-registries",
							DefaultValue: "docker.io,ghcr.io", Description: "permitted image registries",
						}),
					listField("ports", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32,
						&forgepb.ConfigFieldOptions{
							EnvVar: "EXTRA_PORTS", Flag: "extra-ports", Description: "extra listen ports",
						}),
					msgListField("backoffs", 3, ".google.protobuf.Duration",
						&forgepb.ConfigFieldOptions{
							EnvVar: "BACKOFFS", Description: "retry backoff ladder",
						}),
					msgListField("labels", 4, pkg+"ListConfig.LabelsEntry",
						&forgepb.ConfigFieldOptions{
							EnvVar: "LABELS", Description: "a map field — must be rejected, not panic",
						}),
					msgListField("endpoints", 5, pkg+"Endpoint",
						&forgepb.ConfigFieldOptions{
							EnvVar: "ENDPOINTS", Description: "a repeated message — must be rejected, not panic",
						}),
					{
						Name:     proto.String("required_hosts"),
						Number:   proto.Int32(6),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						JsonName: proto.String("required_hosts"),
						Options: configOpt(&forgepb.ConfigFieldOptions{
							EnvVar: "REQUIRED_HOSTS", Required: true, Description: "required list",
						}),
					},
				},
				NestedType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("LabelsEntry"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{
								Name: proto.String("key"), Number: proto.Int32(1),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
								JsonName: proto.String("key"),
							},
							{
								Name: proto.String("value"), Number: proto.Int32(2),
								Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
								Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
								JsonName: proto.String("value"),
							},
						},
						Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
					},
				},
			},
			{
				Name: proto.String("Endpoint"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name: proto.String("url"), Number: proto.Int32(1),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						JsonName: proto.String("url"),
					},
				},
			},
		},
	}

	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().ByName("ListConfig"))
}

// strListOf reads a repeated string field back as a Go slice.
func strListOf(t *testing.T, m protoreflect.Message, name string) []string {
	t.Helper()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		t.Fatalf("no field %q", name)
	}
	l := m.Get(fd).List()
	out := make([]string, 0, l.Len())
	for i := 0; i < l.Len(); i++ {
		out = append(out, l.Get(i).String())
	}
	return out
}

func intListOf(t *testing.T, m protoreflect.Message, name string) []int64 {
	t.Helper()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	l := m.Get(fd).List()
	out := make([]int64, 0, l.Len())
	for i := 0; i < l.Len(); i++ {
		out = append(out, l.Get(i).Int())
	}
	return out
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func eqInts(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The core regression: before the fix this PANICS inside m.Set with
// "type mismatch: cannot convert string to list".
func TestLoadInto_RepeatedStringFromEnv(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example")
	t.Setenv("ALLOWED_REGISTRIES", "docker.io,quay.io,ghcr.io")
	m := buildListTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	want := []string{"docker.io", "quay.io", "ghcr.io"}
	if got := strListOf(t, m, "allowed_registries"); !eqStrs(got, want) {
		t.Errorf("allowed_registries = %q, want %q", got, want)
	}
}

// The default layer has to build a list too — a repeated field's
// default_value is the same comma-separated form.
func TestLoadInto_RepeatedStringDefault(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example")
	m := buildListTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	want := []string{"docker.io", "ghcr.io"}
	if got := strListOf(t, m, "allowed_registries"); !eqStrs(got, want) {
		t.Errorf("allowed_registries default = %q, want %q", got, want)
	}
}

// Every layer replaces the whole list; env does not append to the default.
func TestLoadInto_RepeatedEnvReplacesDefault(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example")
	t.Setenv("ALLOWED_REGISTRIES", "only.example")
	m := buildListTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	want := []string{"only.example"}
	if got := strListOf(t, m, "allowed_registries"); !eqStrs(got, want) {
		t.Errorf("allowed_registries = %q, want %q (env REPLACES the default list)", got, want)
	}
}

// A repeated NON-string field parses each element with its own kind — a
// repeated int32 must not be handed the raw string.
func TestLoadInto_RepeatedInt32FromEnv(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example")
	t.Setenv("EXTRA_PORTS", "8080,9090,7000")
	m := buildListTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	want := []int64{8080, 9090, 7000}
	if got := intListOf(t, m, "ports"); !eqInts(got, want) {
		t.Errorf("ports = %v, want %v", got, want)
	}
}

// One malformed element aborts loading and names the field and env var —
// never a silent drop, matching the scalar contract.
func TestLoadInto_RepeatedInt32MalformedElementErrors(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example")
	t.Setenv("EXTRA_PORTS", "8080,not-a-port")
	m := buildListTestMessage(t)
	err := LoadInto(nil, m.Interface())
	if err == nil {
		t.Fatal("LoadInto: want error for malformed list element, got nil")
	}
	if !contains(err.Error(), "ports") || !contains(err.Error(), "EXTRA_PORTS") {
		t.Errorf("error = %q, want it to name the field (ports) and env var (EXTRA_PORTS)", err.Error())
	}
	// ...and WHICH element, so a long list does not leave the reader hunting.
	if !contains(err.Error(), `element 2 ("not-a-port")`) {
		t.Errorf("error = %q, want it to identify element 2 (\"not-a-port\")", err.Error())
	}
}

// A repeated google.protobuf.Duration is a list of MESSAGES: it pins that the
// cardinality check runs BEFORE the duration branch.
func TestLoadInto_RepeatedDurationFromEnv(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example")
	t.Setenv("BACKOFFS", "1s,5s,30s")
	m := buildListTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	fd := m.Descriptor().Fields().ByName("backoffs")
	l := m.Get(fd).List()
	if l.Len() != 3 {
		t.Fatalf("backoffs len = %d, want 3", l.Len())
	}
	// seconds is field 1 of google.protobuf.Duration.
	secOf := func(i int) int64 {
		dm := l.Get(i).Message()
		return dm.Get(dm.Descriptor().Fields().ByName("seconds")).Int()
	}
	for i, want := range []int64{1, 5, 30} {
		if got := secOf(i); got != want {
			t.Errorf("backoffs[%d] = %ds, want %ds", i, got, want)
		}
	}
}

// Whitespace around separators is formatting, not data: `a, b` in a YAML env
// block means two elements, and a trailing comma is not an empty element.
func TestLoadInto_RepeatedTrimsAndDropsBlanks(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example")
	t.Setenv("ALLOWED_REGISTRIES", "  docker.io ,\tquay.io , ,ghcr.io,")
	m := buildListTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	want := []string{"docker.io", "quay.io", "ghcr.io"}
	if got := strListOf(t, m, "allowed_registries"); !eqStrs(got, want) {
		t.Errorf("allowed_registries = %q, want %q", got, want)
	}
}

// A single element with no separator is a one-element list, not a parse error.
func TestLoadInto_RepeatedSingleElement(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example")
	t.Setenv("ALLOWED_REGISTRIES", "docker.io")
	m := buildListTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	want := []string{"docker.io"}
	if got := strListOf(t, m, "allowed_registries"); !eqStrs(got, want) {
		t.Errorf("allowed_registries = %q, want %q", got, want)
	}
}

// An empty env var is unset for a list as for every other kind, so the
// default stands; the empty FLAG is what clears a defaulted list. Both halves
// are pinned for every kind in empty_env_test.go — this is the case that used
// to go the other way.
func TestLoadInto_RepeatedEmptyEnvKeepsDefault(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example")
	t.Setenv("ALLOWED_REGISTRIES", "")
	m := buildListTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	want := []string{"docker.io", "ghcr.io"}
	if got := strListOf(t, m, "allowed_registries"); !eqStrs(got, want) {
		t.Errorf("allowed_registries = %q, want the default %q (an empty env var is unset)", got, want)
	}

	m = buildListTestMessage(t)
	cmd := newCmd(t, m.Interface())
	if err := cmd.Flags().Set("allowed-registries", ""); err != nil {
		t.Fatal(err)
	}
	if err := LoadInto(cmd, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := strListOf(t, m, "allowed_registries"); len(got) != 0 {
		t.Errorf("allowed_registries = %q, want [] (--allowed-registries= clears the default)", got)
	}
}

// The required check must count ELEMENTS. Value.String() on a list formats as
// "[]", which is non-empty text, so a Kind-only check passes an empty list.
func TestLoadInto_RequiredRepeatedEmptyErrors(t *testing.T) {
	m := buildListTestMessage(t)
	err := LoadInto(nil, m.Interface())
	if err == nil {
		t.Fatal("LoadInto: want error for unset required list, got nil")
	}
	if want := "required config field required_hosts is not set"; !contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err.Error(), want)
	}
}

func TestLoadInto_RequiredRepeatedSatisfiedByEnv(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example,b.example")
	m := buildListTestMessage(t)
	if err := LoadInto(nil, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	want := []string{"a.example", "b.example"}
	if got := strListOf(t, m, "required_hosts"); !eqStrs(got, want) {
		t.Errorf("required_hosts = %q, want %q", got, want)
	}
}

// A map field has no env-var spelling. It must be REJECTED by name, so the
// reader learns which field and what to do instead — not panic, and not the
// old "unsupported field kind message" which named neither.
func TestLoadInto_MapFieldRejectedByName(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example")
	t.Setenv("LABELS", "a=1,b=2")
	m := buildListTestMessage(t)
	err := LoadInto(nil, m.Interface())
	if err == nil {
		t.Fatal("LoadInto: want error for map config field, got nil")
	}
	if !contains(err.Error(), "labels") || !contains(err.Error(), "map") {
		t.Errorf("error = %q, want it to name the field (labels) and say it is a map", err.Error())
	}
	if !contains(err.Error(), "config file") {
		t.Errorf("error = %q, want it to point at the config-file layer", err.Error())
	}
}

// A repeated non-duration message has no env-var spelling either.
func TestLoadInto_RepeatedMessageRejectedByName(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example")
	t.Setenv("ENDPOINTS", "http://a,http://b")
	m := buildListTestMessage(t)
	err := LoadInto(nil, m.Interface())
	if err == nil {
		t.Fatal("LoadInto: want error for repeated message config field, got nil")
	}
	if !contains(err.Error(), "endpoints") {
		t.Errorf("error = %q, want it to name the field (endpoints)", err.Error())
	}
}

// A list field registers as a STRING flag carrying the comma-separated form,
// whatever its element kind — symmetric with the env layer. A pflag
// StringSlice would not round-trip: its Value.String() renders "[a,b]", and
// the loader would then parse the brackets as data. An Int32 flag for a
// repeated int32 could not accept "8080,9090" at all.
func TestRegisterFlags_RepeatedFieldsRegisterAsStringFlags(t *testing.T) {
	m := buildListTestMessage(t)
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	if err := RegisterFlagsFor(fs, m.Interface()); err != nil {
		t.Fatalf("RegisterFlagsFor: %v", err)
	}
	f := fs.Lookup("allowed-registries")
	if f == nil {
		t.Fatal("flag allowed-registries not registered")
	}
	if f.Value.Type() != "string" {
		t.Errorf("allowed-registries flag type = %q, want string", f.Value.Type())
	}
	if f.DefValue != "docker.io,ghcr.io" {
		t.Errorf("allowed-registries default = %q, want docker.io,ghcr.io", f.DefValue)
	}
	p := fs.Lookup("extra-ports")
	if p == nil {
		t.Fatal("flag extra-ports not registered")
	}
	if p.Value.Type() != "string" {
		t.Errorf("extra-ports flag type = %q, want string (not int32 — it carries a list)", p.Value.Type())
	}
}

// The flag layer round-trips a list through the registered string flag and
// still beats env.
func TestLoadInto_RepeatedFlagOverridesEnv(t *testing.T) {
	t.Setenv("REQUIRED_HOSTS", "a.example")
	t.Setenv("ALLOWED_REGISTRIES", "from.env")
	t.Setenv("EXTRA_PORTS", "1111")
	m := buildListTestMessage(t)
	cmd := newCmd(t, m.Interface())
	if err := cmd.Flags().Set("allowed-registries", "from.flag,also.flag"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("extra-ports", "2222,3333"); err != nil {
		t.Fatal(err)
	}
	if err := LoadInto(cmd, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	want := []string{"from.flag", "also.flag"}
	if got := strListOf(t, m, "allowed_registries"); !eqStrs(got, want) {
		t.Errorf("allowed_registries = %q, want %q (flag beats env)", got, want)
	}
	wantPorts := []int64{2222, 3333}
	if got := intListOf(t, m, "ports"); !eqInts(got, wantPorts) {
		t.Errorf("ports = %v, want %v (flag beats env)", got, wantPorts)
	}
}

// ---------------------------------------------------------------------------
// Semantic guards on repeated fields (semantic.go).
//
// Making repeated fields loadable put the role-annotation guards in reach of a
// cardinality they had never seen. They gated on `fd.Kind() == StringKind`,
// which is TRUE for a repeated string, and then read the value with
// Value.String() — which on a list does not fail loudly but returns a dump of
// the list's internal representation ("&{0x... [{...}]}"). A wildcard "*" in a
// repeated CORS-origins field therefore did not match, and the
// wildcard+credentials guard silently passed. These pin both cardinalities.
// ---------------------------------------------------------------------------

// buildRepeatedRoleMessage mirrors buildValidatableMessage but declares the
// role-annotated string fields as REPEATED.
//
// withAllowedValues selects whether the message carries the allowed_values
// field at all. The CORS tests build it WITHOUT, and that is load-bearing
// rather than tidiness: under the unfixed code an UNSET repeated
// allowed_values field still read as a non-empty list dump, so Validate
// returned a spurious error about log_formats and the CORS wildcard test
// passed without ever exercising the CORS guard. Excluding the field is what
// makes those tests fail for exactly one reason.
func buildRepeatedRoleMessage(t *testing.T, withAllowedValues bool) protoreflect.Message {
	t.Helper()

	field := func(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type, repeated bool, opt *forgepb.ConfigFieldOptions) *descriptorpb.FieldDescriptorProto {
		label := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
		if repeated {
			label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
		}
		return &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(num),
			Label:    label.Enum(),
			Type:     typ.Enum(),
			JsonName: proto.String(name),
			Options:  configOpt(opt),
		}
	}

	fields := []*descriptorpb.FieldDescriptorProto{
		field("cors_origins", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, true, &forgepb.ConfigFieldOptions{
			EnvVar: "CORS_ORIGINS", Role: forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_CORS_ORIGINS,
		}),
		field("cors_creds", 2, descriptorpb.FieldDescriptorProto_TYPE_BOOL, false, &forgepb.ConfigFieldOptions{
			EnvVar: "CORS_CREDS", Role: forgepb.ConfigFieldRole_CONFIG_FIELD_ROLE_CORS_ALLOW_CREDENTIALS,
		}),
	}
	name := "AppConfigCORS"
	if withAllowedValues {
		name = "AppConfigAllowed"
		fields = append(fields, field("log_formats", 3, descriptorpb.FieldDescriptorProto_TYPE_STRING, true, &forgepb.ConfigFieldOptions{
			EnvVar: "LOG_FORMAT", AllowedValues: []string{"json", "text"},
		}))
	}

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("config_repeated_roletest_" + name + ".proto"),
		Package: proto.String("config.repeatedroletest." + name),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String(name), Field: fields},
		},
	}

	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().Get(0))
}

func setStrList(t *testing.T, m protoreflect.Message, name string, vals ...string) {
	t.Helper()
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	v := m.NewField(fd)
	for _, s := range vals {
		v.List().Append(protoreflect.ValueOfString(s))
	}
	m.Set(fd, v)
}

// The security-relevant one: a wildcard hiding in a repeated origins list must
// still trip the credentials guard.
//
// log_formats is left UNSET here on purpose. With it populated, the unrelated
// allowed_values check fired first and this test passed against the broken
// code for the wrong reason — the one failure mode a regression test must not
// have. It now fails for exactly one reason: the CORS guard.
func TestValidate_RepeatedCORSWildcardWithCredentials(t *testing.T) {
	m := buildRepeatedRoleMessage(t, false)
	setStrList(t, m, "cors_origins", "https://app.example", "*")
	setBoolField(m, "cors_creds", true)
	if err := Validate(m.Interface()); err == nil {
		t.Error("Validate = nil, want error: wildcard origin in a REPEATED field + credentials is spec-invalid")
	}
}

// ...and a repeated list with no wildcard must still be accepted.
func TestValidate_RepeatedCORSExplicitOriginsWithCredentials(t *testing.T) {
	m := buildRepeatedRoleMessage(t, false)
	setStrList(t, m, "cors_origins", "https://app.example", "https://admin.example")
	setBoolField(m, "cors_creds", true)
	if err := Validate(m.Interface()); err != nil {
		t.Errorf("Validate = %v, want nil for explicit origins + credentials", err)
	}
}

// allowed_values on a repeated field is a closed set PER ELEMENT.
// Only log_formats is populated, so a failure here can only be that check.
func TestValidate_RepeatedAllowedValuesPerElement(t *testing.T) {
	m := buildRepeatedRoleMessage(t, true)
	setStrList(t, m, "log_formats", "json", "text")
	if err := Validate(m.Interface()); err != nil {
		t.Errorf("Validate = %v, want nil (every element in the allowed set)", err)
	}

	setStrList(t, m, "log_formats", "json", "xml")
	err := Validate(m.Interface())
	if err == nil {
		t.Fatal("Validate = nil, want error: element \"xml\" is outside the allowed set")
	}
	if !contains(err.Error(), "xml") {
		t.Errorf("error = %q, want it to name the offending element (xml)", err.Error())
	}
}
