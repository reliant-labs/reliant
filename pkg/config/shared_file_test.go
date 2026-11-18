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
)

// sharedFileTree is the generated shape of one binary with a root config and
// a command config: the root config registered persistent on the root, the
// command config registered LOCAL on `server api` before it is attached (the
// usual construct-then-AddCommand order), and `server worker` loading the root
// config alone. Each RunE loads what its process reads, as the generated
// Load<Msg> calls do.
type sharedFileTree struct {
	root            *cobra.Command
	rootCfg, apiCfg protoreflect.Message
}

func newSharedFileTree(t *testing.T, rootCfg, apiCfg protoreflect.Message) *sharedFileTree {
	t.Helper()
	tr := &sharedFileTree{rootCfg: rootCfg, apiCfg: apiCfg}
	tr.root = &cobra.Command{Use: "app", SilenceUsage: true, SilenceErrors: true}
	if err := RegisterPersistentFlags(tr.root, rootCfg.Interface()); err != nil {
		t.Fatalf("RegisterPersistentFlags: %v", err)
	}
	api := &cobra.Command{Use: "api", RunE: func(cmd *cobra.Command, _ []string) error {
		if err := LoadInto(cmd, tr.rootCfg.Interface()); err != nil {
			return err
		}
		return LoadInto(cmd, tr.apiCfg.Interface())
	}}
	if err := RegisterLocalFlags(api, apiCfg.Interface()); err != nil {
		t.Fatalf("RegisterLocalFlags: %v", err)
	}
	worker := &cobra.Command{Use: "worker", RunE: func(cmd *cobra.Command, _ []string) error {
		return LoadInto(cmd, tr.rootCfg.Interface())
	}}
	server := &cobra.Command{Use: "server"}
	server.AddCommand(api, worker)
	tr.root.AddCommand(server)
	return tr
}

func (tr *sharedFileTree) run(args ...string) error {
	tr.root.SetArgs(args)
	tr.root.SetOut(&bytes.Buffer{})
	tr.root.SetErr(&bytes.Buffer{})
	return tr.root.Execute()
}

// reliantShapedConfigs is a root config and an `api` command config with
// disjoint string leaves, each in its own proto package.
func reliantShapedConfigs(t *testing.T, pkg string) (rootCfg, apiCfg protoreflect.Message) {
	t.Helper()
	rootCfg = buildFlatConfig(t, pkg+".root", "ReliantConfig",
		flatLeaf{"server_url", "SHARED_SERVER_URL", "server-url", "https://default"})
	apiCfg = buildFlatConfig(t, pkg+".api", "ApiServerConfig",
		flatLeaf{"jwks_url", "SHARED_JWKS_URL", "jwks-url", ""})
	return rootCfg, apiCfg
}

// The defect: a process loading its root config AND its command config
// decoded --config into each of them, each rejected the other's keys as
// unknown, and so every non-empty file failed one of the two loads. The file
// is now flat across the configs the command loads, and each takes its own.
func TestConfigFile_RootAndCommandConfigShareOneFile(t *testing.T) {
	for _, doc := range []struct{ name, body string }{
		{"config.yaml", "server_url: https://from-file\njwks_url: https://jwks.example\n"},
		{"config.json", `{"server_url": "https://from-file", "jwks_url": "https://jwks.example"}`},
	} {
		t.Run(doc.name, func(t *testing.T) {
			rootCfg, apiCfg := reliantShapedConfigs(t, "config.sharedfile.ok."+strings.ReplaceAll(doc.name, ".", ""))
			tr := newSharedFileTree(t, rootCfg, apiCfg)
			if err := tr.run("server", "api", "--config", writeFile(t, doc.name, doc.body)); err != nil {
				t.Fatalf("server api --config: %v", err)
			}
			if got := strAt(t, rootCfg, "server_url"); got != "https://from-file" {
				t.Errorf("root config server_url = %q, want the file's value", got)
			}
			if got := strAt(t, apiCfg, "jwks_url"); got != "https://jwks.example" {
				t.Errorf("command config jwks_url = %q, want the file's value", got)
			}
		})
	}
}

// Precedence is unchanged by sharing: a flag of either config still beats
// the file.
func TestConfigFile_SharedFileKeepsPrecedence(t *testing.T) {
	rootCfg, apiCfg := reliantShapedConfigs(t, "config.sharedfile.precedence")
	tr := newSharedFileTree(t, rootCfg, apiCfg)
	path := writeFile(t, "config.yaml", "server_url: https://from-file\njwks_url: https://jwks.file\n")
	if err := tr.run("server", "api", "--config", path, "--jwks-url", "https://jwks.flag"); err != nil {
		t.Fatalf("server api: %v", err)
	}
	if got := strAt(t, apiCfg, "jwks_url"); got != "https://jwks.flag" {
		t.Errorf("jwks_url = %q, want the flag to beat the file", got)
	}
	if got := strAt(t, rootCfg, "server_url"); got != "https://from-file" {
		t.Errorf("server_url = %q, want the file's value", got)
	}
}

// A misspelled key is still a loud error, and it names every config the
// command loads — the set the key was looked up in.
func TestConfigFile_SharedFileRefusesAnUnknownKey(t *testing.T) {
	rootCfg, apiCfg := reliantShapedConfigs(t, "config.sharedfile.typo")
	tr := newSharedFileTree(t, rootCfg, apiCfg)
	err := tr.run("server", "api", "--config", writeFile(t, "config.yaml", "server_url: https://x\njwks_ulr: https://typo\n"))
	if err == nil {
		t.Fatal("a key no loaded config declares must be refused")
	}
	for _, want := range []string{`unknown key "jwks_ulr"`, "ApiServerConfig, ReliantConfig", "(from --config)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// One file serves one PROCESS. `server worker` loads no ApiServerConfig, so
// the api's keys in its file are keys nothing it runs reads — refused, not
// silently ignored.
func TestConfigFile_AnotherProcesssKeyIsRefused(t *testing.T) {
	rootCfg, apiCfg := reliantShapedConfigs(t, "config.sharedfile.worker")
	tr := newSharedFileTree(t, rootCfg, apiCfg)
	err := tr.run("server", "worker", "--config", writeFile(t, "config.yaml", "server_url: https://x\njwks_url: https://api-only\n"))
	if err == nil || !strings.Contains(err.Error(), `unknown key "jwks_url": ReliantConfig has no such field`) {
		t.Fatalf("server worker must refuse the api's key, got: %v", err)
	}
}

// A key BOTH loaded configs declare cannot be routed by a flat file, so it is
// refused rather than handed to one of them (forge generate refuses the pair
// too; this covers a tree assembled by hand).
func TestConfigFile_KeyDeclaredByBothConfigsIsRefused(t *testing.T) {
	rootCfg := buildFlatConfig(t, "config.sharedfile.both.root", "ReliantConfig",
		flatLeaf{"port", "SHARED_ROOT_PORT", "root-port", ""})
	apiCfg := buildFlatConfig(t, "config.sharedfile.both.api", "ApiServerConfig",
		flatLeaf{"port", "SHARED_API_PORT", "api-port", ""})
	tr := newSharedFileTree(t, rootCfg, apiCfg)
	err := tr.run("server", "api", "--config", writeFile(t, "config.yaml", "port: \"8080\"\n"))
	if err == nil || !strings.Contains(err.Error(), `key "port" is declared by both ReliantConfig and ApiServerConfig`) {
		t.Fatalf("an ambiguous key must be refused, got: %v", err)
	}
}

// Below the top level each config decodes its own keys strictly, so a typo
// inside a nested block is as loud as one at the top.
func TestConfigFile_SharedFileNestedTypoIsLoud(t *testing.T) {
	rootCfg := buildFileLayerMessage(t) // AppConfig with a db block
	apiCfg := buildFlatConfig(t, "config.sharedfile.nested.api", "ApiServerConfig",
		flatLeaf{"jwks_url", "SHARED_JWKS_URL", "jwks-url", ""})
	tr := newSharedFileTree(t, rootCfg, apiCfg)
	err := tr.run("server", "api", "--config", writeFile(t, "config.yaml", "db:\n  hots: db.internal\njwks_url: https://x\n"))
	if err == nil || !strings.Contains(err.Error(), "hots") {
		t.Fatalf("a misspelled nested key must be refused, got: %v", err)
	}

	tr = newSharedFileTree(t, buildFileLayerMessage(t), apiCfg)
	if err := tr.run("server", "api", "--config", writeFile(t, "config.yaml", "db:\n  host: db.internal\njwks_url: https://x\n")); err != nil {
		t.Fatalf("a valid nested block beside the command config's key: %v", err)
	}
}

func TestOwnDocument_RefusesMalformedTopLevels(t *testing.T) {
	md := buildFileLayerMessage(t).Descriptor()
	for doc, want := range map[string]string{
		`[1, 2]`:                               "the top level must be a mapping of config keys, got a list",
		`null`:                                 "got an empty document",
		`"log_level"`:                          "got a string",
		`{"log_level": "a", "log_level": "b"}`: `duplicate key "log_level"`,
		`{"log_level": "a"} {}`:                "unexpected content after the top-level mapping",
	} {
		_, err := ownDocument([]byte(doc), md, nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ownDocument(%s) error = %v, want it to contain %q", doc, err, want)
		}
	}
}

// A file may spell a key either way protojson does — the field's JSON name or
// its proto name — so routing a key to its config accepts both.
func TestDeclaresKey_JSONAndProtoNames(t *testing.T) {
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("config_sharedfile_names.proto"),
		Package: proto.String("config.sharedfile.names"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("ReliantConfig"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("server_url"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("serverUrl"),
			}},
		}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatal(err)
	}
	md := fd.Messages().ByName("ReliantConfig")
	for key, want := range map[string]bool{"server_url": true, "serverUrl": true, "ServerUrl": false, "server-url": false} {
		if got := declaresKey(md, key); got != want {
			t.Errorf("declaresKey(%q) = %v, want %v", key, got, want)
		}
	}
}
