package config

import (
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

// buildBinaryConfig compiles a PER-BINARY config message: a shared BaseConfig
// block (the definition every binary composes, holding its own value) plus one
// leaf only this binary reads. pkgName keeps each call in its own proto
// package so two sibling binaries can be built in one test without their
// descriptors colliding in the global registry.
func buildBinaryConfig(t *testing.T, pkgName, msgName, ownField, ownEnv, ownFlag, ownDefault string) protoreflect.Message {
	t.Helper()

	annotated := func(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type, opt *forgepb.ConfigFieldOptions) *descriptorpb.FieldDescriptorProto {
		fdp := &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     typ.Enum(),
			JsonName: proto.String(name),
		}
		fo := &descriptorpb.FieldOptions{}
		proto.SetExtension(fo, forgepb.E_Config, opt)
		fdp.Options = fo
		return fdp
	}

	// The SHARED definition. Values are per-process: each binary composing
	// this block resolves its own log_level.
	baseMsg := &descriptorpb.DescriptorProto{
		Name: proto.String("BaseConfig"),
		Field: []*descriptorpb.FieldDescriptorProto{
			annotated("log_level", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, &forgepb.ConfigFieldOptions{
				EnvVar: "LOG_LEVEL", Flag: "log-level", DefaultValue: "info", Description: "log level",
			}),
		},
	}

	binMsg := &descriptorpb.DescriptorProto{
		Name: proto.String(msgName),
		Field: []*descriptorpb.FieldDescriptorProto{
			{
				Name:     proto.String("base"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
				TypeName: proto.String("." + pkgName + ".BaseConfig"),
				JsonName: proto.String("base"),
			},
			annotated(ownField, 2, descriptorpb.FieldDescriptorProto_TYPE_STRING, &forgepb.ConfigFieldOptions{
				EnvVar: ownEnv, Flag: ownFlag, DefaultValue: ownDefault, Description: "own leaf",
			}),
		},
	}

	fdp := &descriptorpb.FileDescriptorProto{
		Name:        proto.String(pkgName + ".proto"),
		Package:     proto.String(pkgName),
		Syntax:      proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{binMsg, baseMsg},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().ByName(protoreflect.Name(msgName)))
}

func strAt(t *testing.T, m protoreflect.Message, field string) string {
	t.Helper()
	return m.Get(m.Descriptor().Fields().ByName(protoreflect.Name(field))).String()
}

func baseStrAt(t *testing.T, m protoreflect.Message, leaf string) string {
	t.Helper()
	bf := m.Descriptor().Fields().ByName("base")
	sub := m.Get(bf).Message()
	return sub.Get(sub.Descriptor().Fields().ByName(protoreflect.Name(leaf))).String()
}

// The scope split is the whole point: the shared base lands on the root as a
// persistent flag, the binary's own leaf on its subcommand as a local flag,
// and neither half leaks into the other's flag set.
func TestRegisterScoped_PartitionsSharedAndOwn(t *testing.T) {
	m := buildBinaryConfig(t, "config.scopetest.a.v1", "AdminConfig", "admin_api_key", "ADMIN_API_KEY", "admin-api-key", "")

	root := &cobra.Command{Use: "app"}
	sub := &cobra.Command{Use: "admin", RunE: func(*cobra.Command, []string) error { return nil }}
	root.AddCommand(sub)

	if err := RegisterSharedFlags(root, m.Interface()); err != nil {
		t.Fatalf("RegisterSharedFlags: %v", err)
	}
	if err := RegisterOwnFlags(sub, m.Interface()); err != nil {
		t.Fatalf("RegisterOwnFlags: %v", err)
	}

	// Shared half is persistent on the root...
	if f := root.PersistentFlags().Lookup("log-level"); f == nil || f.DefValue != "info" {
		t.Errorf("root persistent --log-level = %v, want default \"info\"", f)
	}
	// ...and NOT a local flag on the root.
	if f := root.Flags().Lookup("admin-api-key"); f != nil {
		t.Errorf("root must not carry the binary's own flag --admin-api-key")
	}
	// Own half is local to the subcommand.
	if f := sub.Flags().Lookup("admin-api-key"); f == nil {
		t.Error("subcommand missing its own --admin-api-key")
	}
	// The subcommand must NOT re-register the shared leaf as its own local
	// flag — that would be two definitions of one value.
	if f := sub.LocalFlags().Lookup("log-level"); f != nil {
		t.Error("subcommand must inherit --log-level, not redeclare it locally")
	}
	// But it must still RESOLVE as an inherited flag, which is what lets one
	// Load call fill both halves once cobra has merged the sets at execution.
	if f := sub.InheritedFlags().Lookup("log-level"); f == nil {
		t.Error("subcommand should see the inherited persistent --log-level")
	}
}

// Precedence must survive the persistent-flag route: a flag set on the root
// still beats an env var, and an UNSET persistent flag must not shadow env
// with its default.
func TestRegisterScoped_PrecedenceThroughPersistentFlag(t *testing.T) {
	t.Setenv("LOG_LEVEL", "warn")

	// Env beats the default when no flag is passed.
	t.Run("env beats default", func(t *testing.T) {
		m := buildBinaryConfig(t, "config.scopetest.b.v1", "AdminConfig", "admin_api_key", "ADMIN_API_KEY", "admin-api-key", "")
		root := &cobra.Command{Use: "app"}
		var got string
		sub := &cobra.Command{Use: "admin", RunE: func(cmd *cobra.Command, _ []string) error {
			if err := LoadInto(cmd, m.Interface()); err != nil {
				return err
			}
			got = baseStrAt(t, m, "log_level")
			return nil
		}}
		root.AddCommand(sub)
		if err := RegisterSharedFlags(root, m.Interface()); err != nil {
			t.Fatal(err)
		}
		if err := RegisterOwnFlags(sub, m.Interface()); err != nil {
			t.Fatal(err)
		}
		root.SetArgs([]string{"admin"})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if got != "warn" {
			t.Errorf("log_level = %q, want %q (env beats proto default)", got, "warn")
		}
	})

	// A persistent flag passed on the command line beats the env var.
	t.Run("flag beats env", func(t *testing.T) {
		m := buildBinaryConfig(t, "config.scopetest.c.v1", "AdminConfig", "admin_api_key", "ADMIN_API_KEY", "admin-api-key", "")
		root := &cobra.Command{Use: "app"}
		var got string
		sub := &cobra.Command{Use: "admin", RunE: func(cmd *cobra.Command, _ []string) error {
			if err := LoadInto(cmd, m.Interface()); err != nil {
				return err
			}
			got = baseStrAt(t, m, "log_level")
			return nil
		}}
		root.AddCommand(sub)
		if err := RegisterSharedFlags(root, m.Interface()); err != nil {
			t.Fatal(err)
		}
		if err := RegisterOwnFlags(sub, m.Interface()); err != nil {
			t.Fatal(err)
		}
		root.SetArgs([]string{"admin", "--log-level", "debug"})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if got != "debug" {
			t.Errorf("log_level = %q, want %q (flag beats env)", got, "debug")
		}
	})
}

// Two binaries composing the SAME base must resolve it independently, and
// neither may see the other's own flags. This is the disjointness claim at
// the CLI level.
func TestRegisterScoped_SiblingBinariesAreDisjoint(t *testing.T) {
	admin := buildBinaryConfig(t, "config.scopetest.d.v1", "AdminConfig", "admin_api_key", "ADMIN_API_KEY", "admin-api-key", "")
	gateway := buildBinaryConfig(t, "config.scopetest.e.v1", "GatewayConfig", "upstream_url", "UPSTREAM_URL", "upstream-url", "http://localhost")

	root := &cobra.Command{Use: "app"}
	adminCmd := &cobra.Command{Use: "admin", RunE: func(*cobra.Command, []string) error { return nil }}
	gwCmd := &cobra.Command{Use: "gateway", RunE: func(*cobra.Command, []string) error { return nil }}
	root.AddCommand(adminCmd, gwCmd)

	if err := RegisterSharedFlags(root, admin.Interface()); err != nil {
		t.Fatal(err)
	}
	if err := RegisterOwnFlags(adminCmd, admin.Interface()); err != nil {
		t.Fatal(err)
	}
	if err := RegisterOwnFlags(gwCmd, gateway.Interface()); err != nil {
		t.Fatal(err)
	}

	if gwCmd.Flags().Lookup("admin-api-key") != nil {
		t.Error("gateway must not see admin's own flag")
	}
	if adminCmd.Flags().Lookup("upstream-url") != nil {
		t.Error("admin must not see gateway's own flag")
	}
	// Each still inherits the shared base. Asserted via InheritedFlags():
	// cobra only folds a parent's persistent flags into a subcommand's
	// Flags() once something triggers the merge (execution, or a
	// LocalFlags/InheritedFlags call), so Flags() alone is not a reliable
	// probe BEFORE Execute. Load runs inside RunE, i.e. after the merge —
	// TestRegisterScoped_PrecedenceThroughPersistentFlag covers that path.
	if adminCmd.InheritedFlags().Lookup("log-level") == nil {
		t.Error("admin should inherit the shared --log-level")
	}
	if gwCmd.InheritedFlags().Lookup("log-level") == nil {
		t.Error("gateway should inherit the shared --log-level")
	}
}

// A sensitive field never gets a flag, whichever scope registers it — the
// existing defense against shell-history / `ps` leaks must survive scoping.
func TestRegisterScoped_SensitiveNeverGetsAFlag(t *testing.T) {
	fo := &descriptorpb.FieldOptions{}
	proto.SetExtension(fo, forgepb.E_Config, &forgepb.ConfigFieldOptions{
		EnvVar: "ADMIN_TOKEN", Flag: "admin-token", Sensitive: true, Description: "secret",
	})
	msg := &descriptorpb.DescriptorProto{
		Name: proto.String("AdminConfig"),
		Field: []*descriptorpb.FieldDescriptorProto{{
			Name:     proto.String("admin_token"),
			Number:   proto.Int32(1),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: proto.String("admin_token"),
			Options:  fo,
		}},
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name:        proto.String("config.scopetest.f.proto"),
		Package:     proto.String("config.scopetest.f.v1"),
		Syntax:      proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{msg},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	m := dynamicpb.NewMessage(fd.Messages().ByName("AdminConfig"))

	sub := &cobra.Command{Use: "admin"}
	if err := RegisterOwnFlags(sub, m.Interface()); err != nil {
		t.Fatalf("RegisterOwnFlags: %v", err)
	}
	if f := sub.Flags().Lookup("admin-token"); f != nil {
		t.Error("sensitive field must never be exposed as a flag")
	}

	// It still resolves from the environment.
	t.Setenv("ADMIN_TOKEN", "s3cret")
	if err := LoadInto(sub, m.Interface()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}
	if got := strAt(t, m, "admin_token"); got != "s3cret" {
		t.Errorf("admin_token = %q, want %q (env/Secret is the only source)", got, "s3cret")
	}
}
