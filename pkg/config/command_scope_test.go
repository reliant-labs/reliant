package config

import (
	"bytes"
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

// flatLeaf is one string config leaf of a flat test message.
type flatLeaf struct{ name, env, flag, def string }

// buildFlatConfig compiles a config message of string leaves, in its own
// proto package so several can coexist in the global registry.
func buildFlatConfig(t *testing.T, pkgName, msgName string, leaves ...flatLeaf) protoreflect.Message {
	t.Helper()
	msg := &descriptorpb.DescriptorProto{Name: proto.String(msgName)}
	for i, l := range leaves {
		fo := &descriptorpb.FieldOptions{}
		proto.SetExtension(fo, forgepb.E_Config, &forgepb.ConfigFieldOptions{
			EnvVar: l.env, Flag: l.flag, DefaultValue: l.def, Description: l.name,
		})
		msg.Field = append(msg.Field, &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(l.name),
			Number:   proto.Int32(int32(i + 1)),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: proto.String(l.name),
			Options:  fo,
		})
	}
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:        proto.String(pkgName + ".proto"),
		Package:     proto.String(pkgName),
		Syntax:      proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{msg},
	}, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return dynamicpb.NewMessage(fd.Messages().ByName(protoreflect.Name(msgName)))
}

// reliantTree is the one-binary-several-processes shape: `app server api`
// and `app server worker`, with a binary-wide root config registered on the
// root and a command config registered on `server api` alone. load* run in
// each process's RunE, the way a generated Load<Msg> does.
type reliantTree struct {
	root, server, api, worker *cobra.Command
	rootCfg, apiCfg           protoreflect.Message
	loaded                    map[string]string
}

func newReliantTree(t *testing.T, pkg string) *reliantTree {
	t.Helper()
	tr := &reliantTree{loaded: map[string]string{}}
	tr.rootCfg = buildFlatConfig(t, pkg+".root", "ReliantConfig",
		flatLeaf{"server_url", "RELIANT_SERVER_URL", "server-url", "https://default"})
	tr.apiCfg = buildFlatConfig(t, pkg+".api", "ApiServerConfig",
		flatLeaf{"grpc_port", "GRPC_PORT", "grpc-port", "9090"})

	tr.root = &cobra.Command{Use: "app", SilenceUsage: true, SilenceErrors: true}
	tr.server = &cobra.Command{Use: "server"}
	tr.api = &cobra.Command{Use: "api", RunE: func(cmd *cobra.Command, _ []string) error {
		if err := LoadInto(cmd, tr.rootCfg.Interface()); err != nil {
			return err
		}
		if err := CheckCommandPath(cmd, "ApiServerConfig", "server", "api"); err != nil {
			return err
		}
		if err := LoadInto(cmd, tr.apiCfg.Interface()); err != nil {
			return err
		}
		tr.loaded["server_url"] = strAt(t, tr.rootCfg, "server_url")
		tr.loaded["grpc_port"] = strAt(t, tr.apiCfg, "grpc_port")
		return nil
	}}
	tr.worker = &cobra.Command{Use: "worker", RunE: func(cmd *cobra.Command, _ []string) error {
		if err := LoadInto(cmd, tr.rootCfg.Interface()); err != nil {
			return err
		}
		tr.loaded["server_url"] = strAt(t, tr.rootCfg, "server_url")
		return nil
	}}
	tr.root.AddCommand(tr.server)
	tr.server.AddCommand(tr.api, tr.worker)

	if err := RegisterPersistentFlags(tr.root, tr.rootCfg.Interface()); err != nil {
		t.Fatalf("RegisterPersistentFlags: %v", err)
	}
	if err := RegisterFlags(tr.api, tr.apiCfg.Interface()); err != nil {
		t.Fatalf("RegisterFlags: %v", err)
	}
	return tr
}

func (tr *reliantTree) run(args ...string) error {
	tr.root.SetArgs(args)
	tr.root.SetOut(&bytes.Buffer{})
	tr.root.SetErr(&bytes.Buffer{})
	return tr.root.Execute()
}

// A ROOT config is the binary-wide surface: its flags are persistent on the
// root, so every subcommand resolves them, on either side of the
// subcommand path.
func TestRegisterPersistentFlags_RootConfigReachesEverySubcommand(t *testing.T) {
	for i, args := range [][]string{
		{"server", "api", "--server-url", "https://flag"},
		{"--server-url", "https://flag", "server", "api"},
		{"server", "worker", "--server-url", "https://flag"},
	} {
		tr := newReliantTree(t, "config.cmdscope.root"+string(rune('a'+i)))
		if err := tr.run(args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if got := tr.loaded["server_url"]; got != "https://flag" {
			t.Errorf("%v: server_url = %q, want the flag value", args, got)
		}
	}
}

// A COMMAND config's flags are local to its subcommand: `server api` takes
// --grpc-port, and `server worker` — a different process — rejects it.
func TestCommandScopedConfig_FlagsAreLocalToTheirSubcommand(t *testing.T) {
	tr := newReliantTree(t, "config.cmdscope.local")
	if err := tr.run("server", "api", "--grpc-port", "7000"); err != nil {
		t.Fatalf("server api --grpc-port: %v", err)
	}
	if got := tr.loaded["grpc_port"]; got != "7000" {
		t.Errorf("grpc_port = %q, want 7000", got)
	}

	tr = newReliantTree(t, "config.cmdscope.local2")
	err := tr.run("server", "worker", "--grpc-port", "7000")
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --grpc-port") {
		t.Fatalf("server worker must not accept the api's --grpc-port, got: %v", err)
	}
	if tr.server.PersistentFlags().Lookup("grpc-port") != nil || tr.root.PersistentFlags().Lookup("grpc-port") != nil {
		t.Error("a command config's flag must not be persistent on any ancestor")
	}
}

func TestCheckCommandPath(t *testing.T) {
	root := &cobra.Command{Use: "reliant"}
	server := &cobra.Command{Use: "server"}
	api := &cobra.Command{Use: "api [flags]"}
	worker := &cobra.Command{Use: "worker"}
	root.AddCommand(server)
	server.AddCommand(api, worker)

	if err := CheckCommandPath(api, "ApiServerConfig", "server", "api"); err != nil {
		t.Errorf("the declared command was refused: %v", err)
	}

	err := CheckCommandPath(worker, "ApiServerConfig", "server", "api")
	if err == nil {
		t.Fatal("loading a command config from another command must be refused")
	}
	for _, want := range []string{
		`ApiServerConfig is declared for the "server api" subcommand`,
		`(forge.v1.binary_config).command = ["server", "api"]`,
		"`reliant server worker` loaded it",
		"call LoadApiServerConfig from the \"server api\" command",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q:\n%s", want, err)
		}
	}

	// The root command itself is not `server api` either.
	if err := CheckCommandPath(server, "ApiServerConfig", "server", "api"); err == nil {
		t.Error("an ancestor of the declared command must be refused: its flags are not registered there")
	}

	// Detached and nil commands have no tree to compare: a test building a
	// bare command, or a non-cobra entrypoint.
	if err := CheckCommandPath(&cobra.Command{Use: "api"}, "ApiServerConfig", "server", "api"); err != nil {
		t.Errorf("a detached command must not be checked: %v", err)
	}
	if err := CheckCommandPath(nil, "ApiServerConfig", "server", "api"); err != nil {
		t.Errorf("a nil command must not be checked: %v", err)
	}
}

// --config is registered once, by the root config, and a command config
// registered beneath it does not shadow it with a second declaration.
func TestRegisterFlags_DoesNotRedeclareAnInheritedConfigFlag(t *testing.T) {
	tr := newReliantTree(t, "config.cmdscope.cfgflag")
	if tr.root.PersistentFlags().Lookup(ConfigFlag) == nil {
		t.Fatal("root config must register --config")
	}
	if tr.api.PersistentFlags().Lookup(ConfigFlag) != nil || tr.api.LocalNonPersistentFlags().Lookup(ConfigFlag) != nil {
		t.Error("the command config must inherit --config, not redeclare it")
	}
}

// RegisterLocalFlags is what a command config registers with when its
// binary has a root config: the leaves alone, never a --config of its own —
// even when called before the subcommand is attached, which is the usual
// construct-then-AddCommand order.
func TestRegisterLocalFlags_RegistersNoConfigFlag(t *testing.T) {
	m := buildFlatConfig(t, "config.cmdscope.localonly", "ApiServerConfig",
		flatLeaf{"grpc_port", "GRPC_PORT", "grpc-port", "9090"})
	api := &cobra.Command{Use: "api"}
	if err := RegisterLocalFlags(api, m.Interface()); err != nil {
		t.Fatalf("RegisterLocalFlags: %v", err)
	}
	if api.Flags().Lookup("grpc-port") == nil {
		t.Error("the command config's leaf must be a local flag")
	}
	if api.PersistentFlags().Lookup(ConfigFlag) != nil || api.Flags().Lookup(ConfigFlag) != nil {
		t.Error("RegisterLocalFlags must not register --config: the root config owns it")
	}
}
