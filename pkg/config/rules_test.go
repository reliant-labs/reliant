package config

import (
	"strings"
	"testing"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// A config's buf.validate rules are part of loading it: a value they refuse
// fails the load, before the process that loaded it does anything — not in
// whatever constructor reads the setting first, which may run after the
// process has migrated a database.

func TestLoadInto_RejectsValueOutsideFieldRule(t *testing.T) {
	t.Setenv("RULES_GRPC_PORT", "0")
	err := LoadInto(nil, buildRulesMessage(t).Interface())
	if err == nil {
		t.Fatal("LoadInto accepted RULES_GRPC_PORT=0, which (buf.validate.field).int32 = {gte: 1, lte: 65535} refuses")
	}
	for _, want := range []string{"grpc_port = 0", "env: RULES_GRPC_PORT", "flag: --rules-grpc-port", "greater than or equal to 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must contain %q; got: %v", want, err)
		}
	}
}

func TestLoadInto_RejectsCrossFieldCELRule(t *testing.T) {
	t.Setenv("RULES_BILLING_PROVIDER", "stripe")
	err := LoadInto(nil, buildRulesMessage(t).Interface())
	if err == nil {
		t.Fatal("LoadInto accepted RULES_BILLING_PROVIDER=apikey with no RULES_STRIPE_SECRET_KEY, which the message's CEL rule refuses")
	}
	if want := "config RulesConfig: BILLING_PROVIDER=stripe requires STRIPE_SECRET_KEY"; !strings.Contains(err.Error(), want) {
		t.Errorf("error must contain %q; got: %v", want, err)
	}
}

func TestLoadInto_AcceptsValuesTheRulesAllow(t *testing.T) {
	t.Setenv("RULES_BILLING_PROVIDER", "stripe")
	t.Setenv("RULES_STRIPE_SECRET_KEY", "a-long-enough-key")
	t.Setenv("RULES_TIMEOUT", "5s")
	if err := LoadInto(nil, buildRulesMessage(t).Interface()); err != nil {
		t.Fatalf("LoadInto = %v, want nil", err)
	}
}

// A duration rule names the refused value in the spelling the environment
// uses, and a sensitive field's value is never printed.
func TestLoadInto_RuleViolationValues(t *testing.T) {
	t.Run("duration", func(t *testing.T) {
		t.Setenv("RULES_TIMEOUT", "-5s")
		err := LoadInto(nil, buildRulesMessage(t).Interface())
		if err == nil || !strings.Contains(err.Error(), "timeout = -5s (env: RULES_TIMEOUT)") {
			t.Errorf("error must name the duration as -5s; got: %v", err)
		}
	})
	t.Run("sensitive", func(t *testing.T) {
		t.Setenv("RULES_STRIPE_SECRET_KEY", "short")
		err := LoadInto(nil, buildRulesMessage(t).Interface())
		if err == nil || !strings.Contains(err.Error(), "config field stripe_secret_key (env: RULES_STRIPE_SECRET_KEY)") {
			t.Errorf("error must name the field and its variable; got: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "short") {
			t.Errorf("error printed a sensitive value; got: %v", err)
		}
	})
}

// Root.Validate checks a rendered environment the way the process would boot
// on it, so it reports every rule a value breaks, not the first.
func TestRootValidate_ReportsEveryRuleViolation(t *testing.T) {
	r := Root{Name: "api", Configs: []RootConfig{{Message: buildRulesMessage(t).Interface()}}}
	problems := r.Validate(map[string]string{
		"RULES_GRPC_PORT":        "70000",
		"RULES_BILLING_PROVIDER": "stripe",
	})
	var port, cel bool
	for _, p := range problems {
		port = port || strings.Contains(p.Error(), "grpc_port = 70000")
		cel = cel || strings.Contains(p.Error(), "BILLING_PROVIDER=stripe requires STRIPE_SECRET_KEY")
	}
	if !port || !cel {
		t.Errorf("Root.Validate must report both the port rule and the CEL rule; got %v", problems)
	}
}

// Validate, for a message built by hand, runs the same rules.
func TestValidate_RunsRules(t *testing.T) {
	m := buildRulesMessage(t)
	m.Set(m.Descriptor().Fields().ByName("grpc_port"), protoreflect.ValueOfInt32(-1))
	if err := Validate(m.Interface()); err == nil {
		t.Fatal("Validate accepted grpc_port = -1")
	}
}

// buildRulesMessage builds
//
//	message RulesConfig {
//	  option (buf.validate.message).cel = {id: "stripe_secret_key_required", ...};
//	  int32 grpc_port = 1;                    // int32 {gte: 1, lte: 65535}, default 8080
//	  string billing_provider = 2;
//	  string stripe_secret_key = 3;               // sensitive; string {min_len: 8}, ignored when empty
//	  google.protobuf.Duration timeout = 4;   // duration {gt: 0s}, default 30s
//	}
func buildRulesMessage(t *testing.T) *dynamicpb.Message {
	t.Helper()
	cfg := func(o *forgepb.ConfigFieldOptions, rules *validate.FieldRules) *descriptorpb.FieldOptions {
		fo := &descriptorpb.FieldOptions{}
		proto.SetExtension(fo, forgepb.E_Config, o)
		if rules != nil {
			proto.SetExtension(fo, validate.E_Field, rules)
		}
		return fo
	}
	field := func(name string, n int32, typ descriptorpb.FieldDescriptorProto_Type, opts *descriptorpb.FieldOptions) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(n), JsonName: proto.String(name),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: typ.Enum(), Options: opts,
		}
	}
	mo := &descriptorpb.MessageOptions{}
	proto.SetExtension(mo, validate.E_Message, &validate.MessageRules{Cel: []*validate.Rule{{
		Id:         proto.String("stripe_secret_key_required"),
		Message:    proto.String("BILLING_PROVIDER=stripe requires STRIPE_SECRET_KEY"),
		Expression: proto.String("this.billing_provider != 'stripe' || this.stripe_secret_key != ''"),
	}}})
	timeout := field("timeout", 4, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE,
		cfg(&forgepb.ConfigFieldOptions{EnvVar: "RULES_TIMEOUT", DefaultValue: "30s"},
			&validate.FieldRules{Type: &validate.FieldRules_Duration{Duration: &validate.DurationRules{
				GreaterThan: &validate.DurationRules_Gt{Gt: durationpb.New(0)},
			}}}))
	timeout.TypeName = proto.String(".google.protobuf.Duration")
	fdp := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("config_rulestest.proto"),
		Package:    proto.String("config.rulestest.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/duration.proto"},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:    proto.String("RulesConfig"),
			Options: mo,
			Field: []*descriptorpb.FieldDescriptorProto{
				field("grpc_port", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32,
					cfg(&forgepb.ConfigFieldOptions{EnvVar: "RULES_GRPC_PORT", Flag: "rules-grpc-port", DefaultValue: "8080"},
						&validate.FieldRules{Type: &validate.FieldRules_Int32{Int32: &validate.Int32Rules{
							GreaterThan: &validate.Int32Rules_Gte{Gte: 1},
							LessThan:    &validate.Int32Rules_Lte{Lte: 65535},
						}}})),
				field("billing_provider", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING,
					cfg(&forgepb.ConfigFieldOptions{EnvVar: "RULES_BILLING_PROVIDER"}, nil)),
				field("stripe_secret_key", 3, descriptorpb.FieldDescriptorProto_TYPE_STRING,
					cfg(&forgepb.ConfigFieldOptions{EnvVar: "RULES_STRIPE_SECRET_KEY", Sensitive: true},
						&validate.FieldRules{
							Ignore: validate.Ignore_IGNORE_IF_ZERO_VALUE.Enum(),
							Type:   &validate.FieldRules_String_{String_: &validate.StringRules{MinLen: proto.Uint64(8)}},
						})),
				timeout,
			},
		}},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().ByName("RulesConfig"))
}
