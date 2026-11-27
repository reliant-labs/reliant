package config

import (
	"bytes"
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

// The alias fixture, shaped after reliant's three real chains: a URL read
// from a primary and two legacy names, a mode with a permanent alias owned by
// another ecosystem (NODE_ENV's role), and a port whose old name is
// deprecated. Every name is prefixed so a developer's own shell cannot supply
// one (NODE_ENV itself is set in plenty of them).
const (
	aliasURL      = "ALIASTEST_CONTROL_PLANE_URL"
	aliasURLAPI   = "ALIASTEST_CONTROL_PLANE_API_URL"
	aliasURLBase  = "ALIASTEST_CONTROL_PLANE_BASE_URL"
	aliasEnv      = "ALIASTEST_ENV"
	aliasNodeEnv  = "ALIASTEST_NODE_ENV"
	aliasPort     = "ALIASTEST_DAEMON_LISTEN_PORT"
	aliasOldPort  = "ALIASTEST_TOOLS_DAEMON_PORT"
	aliasToken    = "ALIASTEST_TOKEN"
	aliasOldToken = "ALIASTEST_LEGACY_TOKEN"
)

var aliasTestEnv = []string{aliasURL, aliasURLAPI, aliasURLBase, aliasEnv, aliasNodeEnv, aliasPort, aliasOldPort, aliasToken, aliasOldToken}

func alias(name string, deprecated bool) *forgepb.EnvVarAlias {
	return &forgepb.EnvVarAlias{Name: name, Deprecated: deprecated}
}

// buildAliasTestMessage compiles the alias fixture: url (two aliases, the
// second deprecated), environment (a permanent alias and a flag), port (a
// deprecated alias, a default and a flag) and a required token with an alias.
func buildAliasTestMessage(t *testing.T) protoreflect.Message {
	t.Helper()
	field := func(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type, opt *forgepb.ConfigFieldOptions) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     typ.Enum(),
			JsonName: proto.String(name),
			Options:  configOpt(opt),
		}
	}
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("config_alias_testcfg.proto"),
		Package: proto.String("config.aliastest.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("AliasConfig"),
			Field: []*descriptorpb.FieldDescriptorProto{
				field("url", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, &forgepb.ConfigFieldOptions{
					EnvVar:        aliasURL,
					EnvVarAliases: []*forgepb.EnvVarAlias{alias(aliasURLAPI, false), alias(aliasURLBase, true)},
				}),
				field("environment", 2, descriptorpb.FieldDescriptorProto_TYPE_STRING, &forgepb.ConfigFieldOptions{
					EnvVar: aliasEnv, Flag: "environment", DefaultValue: "production", Description: "runtime environment",
					EnvVarAliases: []*forgepb.EnvVarAlias{alias(aliasNodeEnv, false)},
				}),
				field("port", 3, descriptorpb.FieldDescriptorProto_TYPE_INT32, &forgepb.ConfigFieldOptions{
					EnvVar: aliasPort, Flag: "port", DefaultValue: "9190", Description: "listen port",
					EnvVarAliases: []*forgepb.EnvVarAlias{alias(aliasOldPort, true)},
				}),
				field("token", 4, descriptorpb.FieldDescriptorProto_TYPE_STRING, &forgepb.ConfigFieldOptions{
					EnvVar: aliasToken, Required: true, Sensitive: true,
					EnvVarAliases: []*forgepb.EnvVarAlias{alias(aliasOldToken, false)},
				}),
			},
		}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().Get(0))
}

// aliasEnvFor leaves exactly the given variables set for the test.
func aliasEnvFor(t *testing.T, set map[string]string) {
	t.Helper()
	for _, k := range aliasTestEnv {
		t.Setenv(k, "") // registers the restore
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range set {
		t.Setenv(k, v)
	}
}

// loadAlias loads the fixture through a command whose stderr is captured,
// returning the message and what the load printed.
func loadAlias(t *testing.T, flags map[string]string) (protoreflect.Message, string, error) {
	t.Helper()
	m := buildAliasTestMessage(t)
	cmd := &cobra.Command{Use: "test"}
	if err := RegisterFlagsFor(cmd.Flags(), m.Interface()); err != nil {
		t.Fatalf("RegisterFlagsFor: %v", err)
	}
	for k, v := range flags {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	err := LoadInto(cmd, m.Interface())
	return m, stderr.String(), err
}

// TestLoadInto_EnvAliasPrecedence: env_var, then each alias in the order
// declared, the first one set winning — and "" is unset at every step, so an
// empty primary falls through exactly as an absent one does. That is the
// hand-written chain the alias replaces: `if v := os.Getenv(A); v != "" …`.
func TestLoadInto_EnvAliasPrecedence(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"primary alone", map[string]string{aliasURL: "https://primary"}, "https://primary"},
		{"primary beats every alias", map[string]string{aliasURL: "https://primary", aliasURLAPI: "https://api", aliasURLBase: "https://base"}, "https://primary"},
		{"first alias when the primary is unset", map[string]string{aliasURLAPI: "https://api", aliasURLBase: "https://base"}, "https://api"},
		{"last alias alone", map[string]string{aliasURLBase: "https://base"}, "https://base"},
		{"an empty primary is unset", map[string]string{aliasURL: "", aliasURLAPI: "https://api"}, "https://api"},
		{"an empty alias is unset too", map[string]string{aliasURL: "", aliasURLAPI: "", aliasURLBase: "https://base"}, "https://base"},
		{"none set", map[string]string{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{aliasToken: "t"}
			for k, v := range tc.env {
				env[k] = v
			}
			aliasEnvFor(t, env)
			m, _, err := loadAlias(t, nil)
			if err != nil {
				t.Fatalf("LoadInto: %v", err)
			}
			if got := strOf(m, "url"); got != tc.want {
				t.Errorf("url = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLoadInto_EnvAliasBelowFlagAboveDefault: an alias is part of the env
// layer, so it beats the default and loses to a flag.
func TestLoadInto_EnvAliasBelowFlagAboveDefault(t *testing.T) {
	aliasEnvFor(t, map[string]string{aliasToken: "t", aliasNodeEnv: "development"})
	m, _, err := loadAlias(t, nil)
	if err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := strOf(m, "environment"); got != "development" {
		t.Errorf("environment = %q, want the alias's development over the default", got)
	}

	m, _, err = loadAlias(t, map[string]string{"environment": "staging"})
	if err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := strOf(m, "environment"); got != "staging" {
		t.Errorf("environment = %q, want the flag's staging over the alias", got)
	}
}

// TestLoadInto_EnvAliasMalformedPrimaryNeverFallsThrough: strict parsing
// holds across names. A malformed value in the name that wins is an error
// naming that variable — never a quiet fall-through to an alias that parses.
func TestLoadInto_EnvAliasMalformedPrimaryNeverFallsThrough(t *testing.T) {
	aliasEnvFor(t, map[string]string{aliasToken: "t", aliasPort: "not-a-port", aliasOldPort: "7000"})
	_, _, err := loadAlias(t, nil)
	if err == nil || !strings.Contains(err.Error(), "from env "+aliasPort) {
		t.Fatalf("want the primary's parse error naming %s, got %v", aliasPort, err)
	}

	aliasEnvFor(t, map[string]string{aliasToken: "t", aliasOldPort: "not-a-port"})
	_, _, err = loadAlias(t, nil)
	if err == nil || !strings.Contains(err.Error(), "from env "+aliasOldPort) {
		t.Fatalf("want the alias's parse error naming %s, got %v", aliasOldPort, err)
	}
}

// TestLoadInto_DeprecatedAliasNotices pins who hears about an alias: a
// deprecated one that is set, every time, whether it supplied the value or
// was shadowed; a permanent one never.
func TestLoadInto_DeprecatedAliasNotices(t *testing.T) {
	t.Run("deprecated alias supplied the value", func(t *testing.T) {
		aliasEnvFor(t, map[string]string{aliasToken: "t", aliasOldPort: "7000"})
		m, stderr, err := loadAlias(t, nil)
		if err != nil {
			t.Fatalf("LoadInto: %v", err)
		}
		if got := intOf(m, "port"); got != 7000 {
			t.Errorf("port = %d, want 7000 from the deprecated alias", got)
		}
		want := "warning: " + aliasOldPort + " is a deprecated name for " + aliasPort + " (config field port); set " + aliasPort + " instead\n"
		if stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	})
	t.Run("deprecated alias shadowed by an earlier name", func(t *testing.T) {
		aliasEnvFor(t, map[string]string{aliasToken: "t", aliasURLAPI: "https://api", aliasURLBase: "https://base"})
		_, stderr, err := loadAlias(t, nil)
		if err != nil {
			t.Fatalf("LoadInto: %v", err)
		}
		for _, want := range []string{aliasURLBase + " is a deprecated name for " + aliasURL, "is ignored, because " + aliasURLAPI + " is set; unset " + aliasURLBase} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		}
		if strings.Count(stderr, "\n") != 1 {
			t.Errorf("want exactly one notice, got %q", stderr)
		}
	})
	t.Run("permanent alias and unset deprecated aliases are silent", func(t *testing.T) {
		aliasEnvFor(t, map[string]string{aliasToken: "t", aliasNodeEnv: "development", aliasURLAPI: "https://api"})
		_, stderr, err := loadAlias(t, nil)
		if err != nil {
			t.Fatalf("LoadInto: %v", err)
		}
		if stderr != "" {
			t.Errorf("a permanent alias must load silently, got %q", stderr)
		}
	})
	t.Run("an empty deprecated alias is unset, so silent", func(t *testing.T) {
		aliasEnvFor(t, map[string]string{aliasToken: "t", aliasOldPort: ""})
		_, stderr, err := loadAlias(t, nil)
		if err != nil {
			t.Fatalf("LoadInto: %v", err)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want nothing for an empty (unset) alias", stderr)
		}
	})
}

// TestLoadInto_RequiredNamesEveryAlias: the required-field error lists every
// variable that would have satisfied it, and which of them were found empty.
func TestLoadInto_RequiredNamesEveryAlias(t *testing.T) {
	aliasEnvFor(t, map[string]string{aliasOldToken: ""})
	_, _, err := loadAlias(t, nil)
	if err == nil {
		t.Fatal("want the required-field error")
	}
	for _, want := range []string{
		"required config field token is not set (env: " + aliasToken + " or " + aliasOldToken + ")",
		aliasOldToken + " is set but empty",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}

	aliasEnvFor(t, map[string]string{aliasOldToken: "from-alias"})
	m, _, err := loadAlias(t, nil)
	if err != nil {
		t.Fatalf("an alias must satisfy a required field: %v", err)
	}
	if got := strOf(m, "token"); got != "from-alias" {
		t.Errorf("token = %q, want from-alias", got)
	}
}

// TestRegisterFlags_UsageNamesTheEnvVars: --help is where an operator learns
// which variables set a flag's field. A permanent alias is listed; a
// deprecated one is not, since a new setter should never pick it.
func TestRegisterFlags_UsageNamesTheEnvVars(t *testing.T) {
	m := buildAliasTestMessage(t)
	cmd := &cobra.Command{Use: "test"}
	if err := RegisterFlagsFor(cmd.Flags(), m.Interface()); err != nil {
		t.Fatal(err)
	}
	if got, want := cmd.Flags().Lookup("environment").Usage, "runtime environment [env: "+aliasEnv+", "+aliasNodeEnv+"]"; got != want {
		t.Errorf("environment usage = %q, want %q", got, want)
	}
	if got, want := cmd.Flags().Lookup("port").Usage, "listen port [env: "+aliasPort+"]"; got != want {
		t.Errorf("port usage = %q, want %q (the deprecated alias stays out of help)", got, want)
	}
}
