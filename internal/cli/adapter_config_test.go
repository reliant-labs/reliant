package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/configproto"
	"github.com/reliant-labs/forge/internal/templates"
)

func writeAdapterConfigFixture(t *testing.T, root string) {
	t.Helper()
	layout := configproto.ForProject(root)
	content, err := templates.ProjectTemplates().Render("config.proto.tmpl", layout.TemplateData("example.com/testproject"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, filepath.FromSlash(layout.Dir()))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.proto"), content, 0644); err != nil {
		t.Fatal(err)
	}
}

// newTestAdapterCmd is the adapter verb's flag surface over runPackageNew:
// --type=adapter plus the shared --base-url / --dry-run flags.
func newTestAdapterCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	cmd := newTestPackageNewCmd("")
	cmd.Flags().String("type", "adapter", "")
	cmdutil.AddPackageScaffoldFlags(cmd)
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func writeTestForgeYAML(t *testing.T) {
	t.Helper()
	if err := os.WriteFile("forge.yaml", []byte("name: testproject\nmodule_path: example.com/testproject\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterConfigMissingRefusesBeforeWritingPackage(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeTestForgeYAML(t)
	cmd := newTestAdapterCmd(t, "--base-url")
	err := cmd.RunE(cmd, []string{"alpha"})
	if err == nil || !strings.Contains(err.Error(), "declare the config root") {
		t.Fatalf("expected actionable missing config refusal, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "alpha")); !os.IsNotExist(err) {
		t.Fatalf("refused scaffold created package: %v", err)
	}
}

func TestAdapterScaffoldRerunIsIdempotent(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.WriteFile("forge.yaml", []byte("name: testproject\nmodule_path: example.com/testproject\n"), 0644); err != nil {
		t.Fatal(err)
	}
	writeAdapterConfigFixture(t, root)
	cmd := newTestAdapterCmd(t, "--base-url")
	if err := cmd.RunE(cmd, []string{"alpha"}); err != nil {
		t.Fatal(err)
	}
	protoPath := filepath.Join(root, filepath.FromSlash(configproto.ForProject(root).RootPath()))
	before, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}
	adapterPath := filepath.Join(root, "internal", "alpha", "adapter.go")
	owned, err := os.ReadFile(adapterPath)
	if err != nil {
		t.Fatal(err)
	}
	owned = append(owned, []byte("\n// hand-owned sentinel\n")...)
	if err := os.WriteFile(adapterPath, owned, 0644); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, []string{"alpha"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rerun changed proto")
	}
	after, err = os.ReadFile(adapterPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(owned) != string(after) {
		t.Fatal("rerun changed owned adapter")
	}
}

// An adapter is not assumed to have a base URL: its downstream may be a queue
// or a vendor SDK. Without --base-url the scaffold edits no config proto,
// declares no Cfg, and needs no AppConfig to exist.
func TestAdapterWithoutBaseURLTouchesNoConfig(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeTestForgeYAML(t)
	writeAdapterConfigFixture(t, root)
	protoPath := filepath.Join(root, filepath.FromSlash(configproto.ForProject(root).RootPath()))
	before, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}

	cmd := newTestAdapterCmd(t)
	if err := cmd.RunE(cmd, []string{"pubsubpull"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("an adapter scaffolded without --base-url edited the config proto:\n%s", after)
	}
	adapter, err := os.ReadFile(filepath.Join(root, "internal", "pubsubpull", "adapter.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"\tCfg *config.", `/pkg/config"`, "BASE_URL", "GetBaseUrl"} {
		if strings.Contains(string(adapter), unwanted) {
			t.Errorf("adapter.go without --base-url contains %q:\n%s", unwanted, adapter)
		}
	}

	// And with no AppConfig at all, which --base-url would refuse.
	bare := t.TempDir()
	t.Chdir(bare)
	writeTestForgeYAML(t)
	cmd = newTestAdapterCmd(t)
	if err := cmd.RunE(cmd, []string{"queue"}); err != nil {
		t.Fatalf("an adapter without --base-url must not need an AppConfig: %v", err)
	}
}

// --base-url binds <NAME>_BASE_URL; --base-url-env=ENV binds the env var
// given (with or without --base-url); a malformed name is refused before
// anything is written.
func TestAdapterBaseURLNamesItsEnvVar(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		wantEnv string
		wantErr string
	}{
		{args: []string{"--base-url"}, wantEnv: "STRIPE_BASE_URL"},
		{args: []string{"--base-url-env=STRIPE_API_URL"}, wantEnv: "STRIPE_API_URL"},
		{args: []string{"--base-url", "--base-url-env=STRIPE_API_URL"}, wantEnv: "STRIPE_API_URL"},
		{args: []string{"--base-url-env=stripe-url"}, wantErr: "env var name"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			writeTestForgeYAML(t)
			writeAdapterConfigFixture(t, root)
			cmd := newTestAdapterCmd(t, tc.args...)
			err := cmd.RunE(cmd, []string{"stripe"})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one mentioning %q", err, tc.wantErr)
				}
				if _, statErr := os.Stat(filepath.Join(root, "internal", "stripe")); !os.IsNotExist(statErr) {
					t.Errorf("a refused scaffold created the package: %v", statErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			proto, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(configproto.ForProject(root).RootPath())))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(proto), `env_var: "`+tc.wantEnv+`"`) {
				t.Errorf("config proto does not bind base_url to %s:\n%s", tc.wantEnv, proto)
			}
			adapter, err := os.ReadFile(filepath.Join(root, "internal", "stripe", "adapter.go"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(adapter), "Cfg *config.StripeConfig") || !strings.Contains(string(adapter), tc.wantEnv) {
				t.Errorf("adapter.go does not take config.StripeConfig bound to %s:\n%s", tc.wantEnv, adapter)
			}
		})
	}
}

// --base-url means nothing to a non-adapter package.
func TestBaseURLRefusedOffAnAdapter(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeTestForgeYAML(t)
	cmd := newTestPackageNewCmd("")
	cmdutil.AddPackageScaffoldFlags(cmd)
	if err := cmd.Flags().Parse([]string{"--base-url"}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, []string{"cache"}); err == nil || !strings.Contains(err.Error(), "--type=adapter") {
		t.Fatalf("err = %v, want --base-url refused on a service package", err)
	}
}

// --dry-run writes nothing — no package directory, no config proto edit —
// and names every file the real run writes.
func TestAdapterDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	writeTestForgeYAML(t)
	writeAdapterConfigFixture(t, root)
	protoPath := filepath.Join(root, filepath.FromSlash(configproto.ForProject(root).RootPath()))
	before, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}

	cmd := newTestAdapterCmd(t, "--base-url", "--dry-run")
	out := captureStdout(t, func() {
		if err := cmd.RunE(cmd, []string{"stripe"}); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(filepath.Join(root, "internal", "stripe")); !os.IsNotExist(err) {
		t.Fatalf("--dry-run created the package directory: %v", err)
	}
	after, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("--dry-run edited the config proto")
	}
	for _, want := range []string{"Would declare message StripeConfig", "STRIPE_BASE_URL",
		"internal/stripe/adapter.go", "internal/stripe/contract.go", "internal/stripe/observe_chain.go", "nothing was written"} {
		if !strings.Contains(out, want) {
			t.Errorf("--dry-run output missing %q:\n%s", want, out)
		}
	}

	// The plan names the same files the real run then writes.
	real := newTestAdapterCmd(t, "--base-url")
	if err := real.RunE(real, []string{"stripe"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "internal", "stripe"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.Contains(out, "internal/stripe/"+e.Name()) {
			t.Errorf("the real run wrote %s, which --dry-run did not name:\n%s", e.Name(), out)
		}
	}
}
