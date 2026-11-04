package config

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/reliant-labs/forge/pkg/forgepb"
)

// buildSignerMessage is the shape of the two control-plane delivery-signer
// boot failures of 2026-11-03:
//
//	message SignerConfig {
//	  option (buf.validate.message).cel = {id: "signer_needs_registry", ...};
//	  string internal_service_secret = 1; // CHK_INTERNAL_SERVICE_SECRET, sensitive, required
//	  bool   signer_enabled          = 2; // CHK_SIGNER_ENABLED
//	  string registry_base           = 3; // CHK_REGISTRY_BASE
//	  string log_level               = 4; // CHK_LOG_LEVEL, allowed_values
//	}
func buildSignerMessage(t *testing.T) *dynamicpb.Message {
	t.Helper()
	field := func(name string, n int32, typ descriptorpb.FieldDescriptorProto_Type, o *forgepb.ConfigFieldOptions) *descriptorpb.FieldDescriptorProto {
		fo := &descriptorpb.FieldOptions{}
		proto.SetExtension(fo, forgepb.E_Config, o)
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(n), JsonName: proto.String(name),
			Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(), Type: typ.Enum(), Options: fo,
		}
	}
	mo := &descriptorpb.MessageOptions{}
	proto.SetExtension(mo, validate.E_Message, &validate.MessageRules{Cel: []*validate.Rule{{
		Id:         proto.String("signer_needs_registry"),
		Message:    proto.String("delivery_signing.signer_enabled needs image_build.registry_base"),
		Expression: proto.String("!this.signer_enabled || this.registry_base != ''"),
	}}})
	str, boolean := descriptorpb.FieldDescriptorProto_TYPE_STRING, descriptorpb.FieldDescriptorProto_TYPE_BOOL
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("config_checktest.proto"),
		Package: proto.String("config.checktest.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:    proto.String("SignerConfig"),
			Options: mo,
			Field: []*descriptorpb.FieldDescriptorProto{
				field("internal_service_secret", 1, str, &forgepb.ConfigFieldOptions{EnvVar: "CHK_INTERNAL_SERVICE_SECRET", Sensitive: true, Required: true}),
				field("signer_enabled", 2, boolean, &forgepb.ConfigFieldOptions{EnvVar: "CHK_SIGNER_ENABLED"}),
				field("registry_base", 3, str, &forgepb.ConfigFieldOptions{EnvVar: "CHK_REGISTRY_BASE"}),
				field("log_level", 4, str, &forgepb.ConfigFieldOptions{EnvVar: "CHK_LOG_LEVEL", DefaultValue: "info", AllowedValues: []string{"debug", "info"}}),
			},
		}},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().ByName("SignerConfig"))
}

// runCheck loads a fresh SignerConfig under the boot config check with env
// as the process environment and returns the exit code and the result.
func runCheck(t *testing.T, env map[string]string) (int, CheckResult) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	var out bytes.Buffer
	code := -1
	prevExit, prevOut := checkExit, checkOut
	checkExit, checkOut = func(c int) { code = c }, &out
	checkOnce, checkActive = sync.Once{}, nil
	t.Cleanup(func() {
		checkExit, checkOut = prevExit, prevOut
		checkOnce, checkActive = sync.Once{}, nil
	})
	_ = LoadInto(nil, buildSignerMessage(t))
	r, ok := ParseCheckResult(out.String())
	if !ok {
		t.Fatalf("no %s line in %q", CheckResultPrefix, out.String())
	}
	return code, r
}

// The failure shape of control-plane #1886: the env never wires the secret
// the typed config requires. The check finds it with no secret value.
func TestCheckFindsAnUnwiredRequiredSecret(t *testing.T) {
	code, r := runCheck(t, map[string]string{CheckEnv: "1"})
	if code != 1 || r.OK {
		t.Fatalf("exit %d ok=%v, want exit 1 and not ok: %+v", code, r.OK, r)
	}
	if len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "required config field internal_service_secret is not set") {
		t.Fatalf("problems = %q, want the unset internal_service_secret", r.Problems)
	}
}

// The failure shape of control-plane #1890: the secret is wired (a
// placeholder stands in for its value), the signer is on, and the env left
// the registry base the signer needs empty. The message rule sees it.
func TestCheckFindsACrossFieldRuleTheEnvBreaks(t *testing.T) {
	code, r := runCheck(t, map[string]string{
		CheckEnv:                      "1",
		CheckPresentEnv:               "CHK_INTERNAL_SERVICE_SECRET",
		"CHK_INTERNAL_SERVICE_SECRET": CheckPlaceholder,
		"CHK_SIGNER_ENABLED":          "true",
	})
	if code != 1 || r.OK {
		t.Fatalf("exit %d ok=%v, want exit 1: %+v", code, r.OK, r)
	}
	if len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "signer_enabled needs image_build.registry_base") {
		t.Fatalf("problems = %q, want only the signer rule (the wired secret counts as set)", r.Problems)
	}
}

func TestCheckPassesAWellWiredEnv(t *testing.T) {
	code, r := runCheck(t, map[string]string{
		CheckEnv:                      "1",
		CheckPresentEnv:               "CHK_INTERNAL_SERVICE_SECRET",
		"CHK_INTERNAL_SERVICE_SECRET": CheckPlaceholder,
		"CHK_SIGNER_ENABLED":          "true",
		"CHK_REGISTRY_BASE":           "registry.example.com",
	})
	if code != 0 || !r.OK || len(r.Problems) != 0 {
		t.Fatalf("exit %d, result %+v, want exit 0 and ok", code, r)
	}
	if len(r.Configs) != 1 || r.Configs[0] != "config.checktest.v1.SignerConfig" {
		t.Fatalf("configs = %q, want the one loaded", r.Configs)
	}
}

// Every problem is reported, not the first, and a placeholder's own value is
// never judged: a closed set it would fail is not a deployment mistake.
func TestCheckReportsEveryProblemAndNeverJudgesAPlaceholder(t *testing.T) {
	code, r := runCheck(t, map[string]string{
		CheckEnv:             "1",
		CheckPresentEnv:      "CHK_LOG_LEVEL",
		"CHK_LOG_LEVEL":      CheckPlaceholder,
		"CHK_SIGNER_ENABLED": "true",
	})
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	joined := strings.Join(r.Problems, "\n")
	for _, want := range []string{"internal_service_secret is not set", "needs image_build.registry_base"} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "allowed values") {
		t.Errorf("a placeholder's value was judged against allowed_values:\n%s", joined)
	}
}

// A value the env sets that does not parse is a problem the check reports,
// like any other the load collects.
func TestCheckFailsOnAMalformedEnvValue(t *testing.T) {
	code, r := runCheck(t, map[string]string{
		CheckEnv:                      "1",
		CheckPresentEnv:               "CHK_INTERNAL_SERVICE_SECRET",
		"CHK_INTERNAL_SERVICE_SECRET": CheckPlaceholder,
		"CHK_SIGNER_ENABLED":          "maybe",
	})
	if code != 1 || r.OK {
		t.Fatalf("exit %d ok=%v, want exit 1 and not ok: %+v", code, r.OK, r)
	}
	if !strings.Contains(strings.Join(r.Problems, "\n"), `invalid value "maybe" for config field signer_enabled`) {
		t.Fatalf("problems = %q, want the malformed signer_enabled", r.Problems)
	}
}

// A stage that ends the load by returning its error rather than reporting it
// (a refused build-defaults stamp, a default that does not parse, a family
// prefix nothing claims) still fails the check: finish takes the load's
// returned error as a problem, so the check fails closed.
func TestCheckFailsClosedOnAnErrorTheLoadReturns(t *testing.T) {
	SetBuildDefaults("CHK_SIGNER_ENABLED=maybe", buildSignerMessage(t))
	t.Cleanup(func() { SetBuildDefaults("") })
	code, r := runCheck(t, map[string]string{
		CheckEnv:                      "1",
		CheckPresentEnv:               "CHK_INTERNAL_SERVICE_SECRET",
		"CHK_INTERNAL_SERVICE_SECRET": CheckPlaceholder,
	})
	if code != 1 || r.OK || len(r.Problems) == 0 {
		t.Fatalf("exit %d, result %+v, want exit 1, not ok, and the stamp's problem", code, r)
	}
	if !strings.Contains(strings.Join(r.Problems, "\n"), "CHK_SIGNER_ENABLED") {
		t.Fatalf("problems = %q, want the refused build default", r.Problems)
	}
}

// A command that registers a config it never loads has not had that config
// verified: when the settle elapses the check fails, it never reports ok.
func TestCheckFailsWhenARegisteredConfigNeverLoads(t *testing.T) {
	t.Setenv(CheckEnv, "1")
	t.Setenv(CheckPresentEnv, "CHK_INTERNAL_SERVICE_SECRET")
	t.Setenv("CHK_INTERNAL_SERVICE_SECRET", CheckPlaceholder)
	var (
		mu   sync.Mutex
		out  bytes.Buffer
		code = -1
		done = make(chan struct{})
	)
	prevExit, prevOut, prevSettle := checkExit, checkOut, checkSettle
	checkExit = func(c int) { mu.Lock(); code = c; mu.Unlock(); close(done) }
	checkOut, checkSettle = &out, 10*time.Millisecond
	checkOnce, checkActive = sync.Once{}, nil
	t.Cleanup(func() {
		checkExit, checkOut, checkSettle = prevExit, prevOut, prevSettle
		checkOnce, checkActive = sync.Once{}, nil
	})

	cmd := &cobra.Command{Use: "svc", Annotations: map[string]string{
		fileMessagesAnnotation: "config.checktest.v1.SignerConfig,config.checktest.v1.NeverLoaded",
	}}
	if err := LoadInto(cmd, buildSignerMessage(t)); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the check never settled")
	}
	mu.Lock()
	defer mu.Unlock()
	r, ok := ParseCheckResult(out.String())
	if !ok {
		t.Fatalf("no %s line in %q", CheckResultPrefix, out.String())
	}
	if code != 1 || r.OK {
		t.Fatalf("exit %d, result %+v, want exit 1 and not ok", code, r)
	}
	if len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "config.checktest.v1.NeverLoaded: registered on the command but never loaded") {
		t.Fatalf("problems = %q, want the unloaded config", r.Problems)
	}
}

// Without the mode a load is an ordinary load: the first problem is the
// error, nothing is printed and nothing exits.
func TestLoadWithoutTheCheckIsUnchanged(t *testing.T) {
	var out bytes.Buffer
	exited := false
	prevExit, prevOut := checkExit, checkOut
	checkExit, checkOut = func(int) { exited = true }, &out
	checkOnce, checkActive = sync.Once{}, nil
	t.Cleanup(func() { checkExit, checkOut = prevExit, prevOut; checkOnce, checkActive = sync.Once{}, nil })

	err := LoadInto(nil, buildSignerMessage(t))
	if err == nil || !strings.Contains(err.Error(), "internal_service_secret is not set") {
		t.Fatalf("err = %v, want the required-field error", err)
	}
	if exited || out.Len() > 0 {
		t.Fatalf("an ordinary load printed %q / exited %v", out.String(), exited)
	}
}

func TestParseCheckResultTakesTheLastLine(t *testing.T) {
	out := "log line\n" + CheckResultPrefix + ` {"ok":false}` + "\nnoise\n" + CheckResultPrefix + ` {"ok":true,"configs":["a.B"]}` + "\n"
	r, ok := ParseCheckResult(out)
	if !ok || !r.OK || len(r.Configs) != 1 {
		t.Fatalf("ParseCheckResult = %+v, %v", r, ok)
	}
	if _, ok := ParseCheckResult("no result here"); ok {
		t.Fatal("found a result in output with none")
	}
}
