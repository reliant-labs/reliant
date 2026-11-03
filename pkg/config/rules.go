package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// validateRules runs the buf.validate (protovalidate) rules a config message
// declares, as the last step of every load: a value its rules refuse fails
// the load exactly as an out-of-set allowed_values does — before the process
// that loaded it touches anything, and in Root.Validate's deploy-time check
// over a rendered environment, from the same declaration.
//
// The rules are the config's own invariants, written where the fields are:
//
//	int32 grpc_port = 1 [
//	  (forge.v1.config) = {env_var: "GRPC_PORT", default_value: "8080"},
//	  (buf.validate.field).int32 = {gte: 1, lte: 65535}
//	];
//	option (buf.validate.message).cel = {
//	  id: "stripe_secret_key_required",
//	  message: "BILLING_PROVIDER=stripe requires STRIPE_SECRET_KEY",
//	  expression: "this.billing_provider != 'stripe' || this.stripe_secret_key != ''"
//	};
//
// so a cross-field check — one provider needing another field's credential — is
// declared in the proto rather than coded into whatever constructor reads
// the config first, which may run after the process has already migrated a
// database or dialled a broker. Presence stays `required`'s
// ((forge.v1.config).required, narrowed by RequiredWithin to what a root
// reaches); buf.validate's own `required` would demand the field of every
// process that loads the message.
//
// Every field validates, reached or not, as every field loads: a field a
// root does not reach still holds its default or what the environment set.
//
// Each violation is reported on its own (Root.Validate collects them all),
// naming the field by path with the variable and flag that set it, and its
// value unless the field is sensitive.
func validateRules(msg proto.Message, report func(error) error) error {
	err := protovalidate.Validate(msg)
	if err == nil {
		return nil
	}
	var verr *protovalidate.ValidationError
	if !errors.As(err, &verr) {
		// A rule that does not compile, or fails to evaluate, is the
		// config proto's defect: no value could satisfy it.
		return report(fmt.Errorf("config %s: its buf.validate rules cannot be evaluated: %w",
			msg.ProtoReflect().Descriptor().Name(), err))
	}
	for _, v := range verr.Violations {
		if rerr := report(ruleViolation(msg.ProtoReflect().Descriptor(), v)); rerr != nil {
			return rerr
		}
	}
	return nil
}

// ruleViolation phrases one violation for the operator who set the value:
//
//	config field grpc_port = 0 (env: GRPC_PORT, flag: --grpc-port): value must be greater than or equal to 1 and less than or equal to 65535
//	config AppConfig: BILLING_PROVIDER=stripe requires STRIPE_SECRET_KEY
//
// A message-level rule names the message (or, in a nested block, the
// block's path) rather than a field.
func ruleViolation(root protoreflect.MessageDescriptor, v *protovalidate.Violation) error {
	what := v.Proto.GetMessage()
	if what == "" {
		what = "violates rule " + v.Proto.GetRuleId()
	}
	path := protovalidate.FieldPathString(v.Proto.GetField())
	fd := v.FieldDescriptor
	if fd == nil || isConfigBlock(fd) {
		if path == "" {
			path = string(root.Name())
		}
		return fmt.Errorf("config %s: %s", path, what)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "config field %s", path)
	opt := fieldOptions(fd)
	if !opt.GetSensitive() && v.FieldValue.IsValid() && !fd.IsList() && !fd.IsMap() {
		fmt.Fprintf(&b, " = %s", ruleValue(fd, v.FieldValue))
	}
	var sources []string
	if env := envSourcesPhrase(opt); env != "" {
		sources = append(sources, "env: "+env)
	}
	if flag := opt.GetFlag(); flag != "" {
		sources = append(sources, "flag: --"+flag)
	}
	if len(sources) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(sources, ", "))
	}
	fmt.Fprintf(&b, ": %s", what)
	return errors.New(b.String())
}

// ruleValue renders a refused value as it would be written in the
// environment: a string quoted, a duration in Go's spelling ("-5s").
func ruleValue(fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	switch {
	case fd.Kind() == protoreflect.StringKind:
		return fmt.Sprintf("%q", v.String())
	case isDurationField(fd):
		m := v.Message()
		secs := m.Get(m.Descriptor().Fields().ByName("seconds")).Int()
		nanos := m.Get(m.Descriptor().Fields().ByName("nanos")).Int()
		return (time.Duration(secs)*time.Second + time.Duration(nanos)).String()
	case fd.Kind() == protoreflect.MessageKind:
		return string(fd.Message().Name())
	default:
		return v.String()
	}
}
