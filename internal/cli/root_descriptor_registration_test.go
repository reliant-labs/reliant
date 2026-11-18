package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// Embedded Reliant calls NewRootCmd, not Execute (newTestRootCmd is NewRootCmd
// plus the route restore every root-building test needs). Do not exec a forge child:
// the regression is the constructor's registration, before any command runs.
func TestNewRootCmdRegistersDescriptorRefreshForEmbeddedHosts(t *testing.T) {
	codegen.RegisterDescriptorBuilder(nil)
	t.Cleanup(func() { codegen.RegisterDescriptorBuilder(buildDescriptorOnDemand) })
	_ = newTestRootCmd(t)
	if !codegen.DescriptorBuilderRegistered() {
		t.Fatal("embedded command tree has no descriptor extractor; stale proto cache will block lint")
	}
}

func TestDescriptorRefreshAfterProtoEditKeepsGeneratedStampStale(t *testing.T) {
	dir := t.TempDir()
	proto := filepath.Join(dir, "proto", "widget.proto")
	if err := os.MkdirAll(filepath.Dir(proto), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proto, []byte("service Before {}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := codegen.WriteDescriptor(dir, nil); err != nil {
		t.Fatal(err)
	}
	if err := codegen.WriteGeneratedFrom(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proto, []byte("service After {}"), 0644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	codegen.RegisterDescriptorBuilder(func(root string) error { calls++; return codegen.WriteDescriptor(root, nil) })
	t.Cleanup(func() { codegen.RegisterDescriptorBuilder(buildDescriptorOnDemand) })
	for range 2 {
		if _, err := codegen.LoadDescriptor(dir); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("cache refresh calls=%d, want exactly one", calls)
	}
	if drift := codegen.DescriptorDrift(dir, codegen.ReadGeneratedFrom(dir)); !strings.Contains(drift, "edited") {
		t.Fatalf("metadata refresh blessed stale generated code: %q", drift)
	}
}
