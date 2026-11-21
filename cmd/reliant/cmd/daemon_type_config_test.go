// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/reliant-labs/forge/pkg/forgepb"
	"github.com/reliant-labs/forge/pkg/kcleval"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "github.com/reliant-labs/reliant/gen/reliant/config/v1" // registers the config protos the walk reads
)

// RELIANT_DAEMON_TYPE is declared by exactly two configs whose processes never
// meet: DaemonConfig (`daemon start`, the declaration since before v1, which a
// deployment's KCL sets as daemon_config.daemon_type) and AuthConfig (`reliant
// auth`). Declaring it on a config `daemon start` also loads (CLIConfig, the
// root config) would leave `daemon start` two declarations, which forge
// refuses, and moving it off DaemonConfig would silently drop the variable
// from every KCL config that sets daemon_config.daemon_type.
func TestDaemonTypeHasOneDeclarationPerProcess(t *testing.T) {
	declaredBy := map[string]*forgepb.BinaryConfigOptions{}
	protoregistry.GlobalFiles.RangeFilesByPackage("reliant.config.v1", func(fd protoreflect.FileDescriptor) bool {
		msgs := fd.Messages()
		for i := 0; i < msgs.Len(); i++ {
			md := msgs.Get(i)
			binding, _ := proto.GetExtension(md.Options(), forgepb.E_BinaryConfig).(*forgepb.BinaryConfigOptions)
			if binding == nil || binding.GetBinary() == "" {
				continue
			}
			if slices.Contains(configEnvVars(md), "RELIANT_DAEMON_TYPE") {
				declaredBy[string(md.Name())] = binding
			}
		}
		return true
	})
	require.ElementsMatch(t, []string{"AuthConfig", "DaemonConfig"}, keysOf(declaredBy),
		"RELIANT_DAEMON_TYPE is declared by DaemonConfig (daemon start) and AuthConfig (reliant auth) alone")
	require.Equal(t, []string{"daemon", "start"}, declaredBy["DaemonConfig"].GetCommand())
	subtrees := declaredBy["AuthConfig"].GetSubtrees()
	require.Len(t, subtrees, 1)
	require.Equal(t, []string{"auth"}, subtrees[0].GetPath(),
		"AuthConfig's scope must not reach `daemon start`, which DaemonConfig's declaration serves")
}

// RELIANT_DAEMON_TYPE keeps working exactly as before for the `reliant auth`
// commands, now through AuthConfig: managed is a cloud machine, anything else
// allowed is not, and a value outside the allowed set is refused.
func TestAuthCommandsReadRELIANTDaemonType(t *testing.T) {
	for _, path := range [][]string{{"auth", "login"}, {"auth", "logout"}, {"auth", "token", "create"}} {
		cmd, _, err := NewRootCmd().Find(path)
		require.NoError(t, err)

		t.Setenv("RELIANT_DAEMON_TYPE", "")
		require.NoError(t, os.Unsetenv("RELIANT_DAEMON_TYPE"))
		managed, err := isManagedMachine(cmd)
		require.NoError(t, err, path)
		assert.False(t, managed, path)

		t.Setenv("RELIANT_DAEMON_TYPE", "managed")
		managed, err = isManagedMachine(cmd)
		require.NoError(t, err, path)
		assert.True(t, managed, path)

		t.Setenv("RELIANT_DAEMON_TYPE", "self_hosted")
		managed, err = isManagedMachine(cmd)
		require.NoError(t, err, path)
		assert.False(t, managed, path)

		t.Setenv("RELIANT_DAEMON_TYPE", "cloud")
		_, err = isManagedMachine(cmd)
		require.Error(t, err, path)
	}
}

// configEnvVars lists every env var a config message reads, its composed
// blocks' included.
func configEnvVars(md protoreflect.MessageDescriptor) []string {
	var out []string
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if opts, _ := proto.GetExtension(f.Options(), forgepb.E_Config).(*forgepb.ConfigFieldOptions); opts != nil {
			out = append(out, opts.GetEnvVar())
			for _, alias := range opts.GetEnvVarAliases() {
				out = append(out, alias.GetName())
			}
		}
		if f.Kind() == protoreflect.MessageKind && f.Message().FullName().Parent() == md.FullName().Parent() {
			out = append(out, configEnvVars(f.Message())...)
		}
	}
	return out
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestDaemonTypeProjectsThroughKCL pins the KCL side on the path a workload
// takes (deploy/kcl's reliantProcessEnvMap, generated from the protos):
// daemon_config.daemon_type alone still puts RELIANT_DAEMON_TYPE in a `daemon
// start` process's env, as it did before AuthConfig existed, and
// auth_config.daemon_type alone puts it in a `reliant auth` process's env.
// Each reaches only its own process.
func TestDaemonTypeProjectsThroughKCL(t *testing.T) {
	testkit.Slow(t, "evaluates deploy/kcl through `forge kcl eval`",
		testkit.Inputs("deploy/kcl/**"))
	project := kcleval.Options{ProjectDir: "../../..", Binary: pinnedForge(t)}

	var cases map[string]map[string]any
	require.NoError(t, kcleval.Select(context.Background(), "deploy/kcl/testdata/daemon_type.k", "cases", &cases, project))

	for name, want := range map[string]string{
		"daemon_start_daemon_field": "managed",
		"daemon_start_auth_field":   "",
		"auth_login_auth_field":     "managed",
		"auth_login_daemon_field":   "",
	} {
		env, ok := cases[name]
		require.True(t, ok, "fixture case %s", name)
		got, declared := env["RELIANT_DAEMON_TYPE"]
		require.True(t, declared, "%s: RELIANT_DAEMON_TYPE is projected: %v", name, env)
		require.Equal(t, want, got, "%s: RELIANT_DAEMON_TYPE", name)
	}
}
