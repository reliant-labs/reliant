package codegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The descriptor is a DERIVED CACHE of the protos. A cache that cannot detect
// its own staleness does not degrade — it lies.
//
// Measured before this guard existed: renaming one RPC in the descriptor,
// touching no .proto, made `forge project graph` report an RPC that existed in
// no proto file and omit one that existed in six places. Nothing warned,
// because loading was read-file-and-unmarshal with no validation.
func TestLoadDescriptor_RefusesStaleCache(t *testing.T) {
	dir := t.TempDir()
	protoDir := filepath.Join(dir, "proto", "services", "widget", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	proto := "service WidgetService {\n  rpc GetWidget(GetWidgetRequest) returns (GetWidgetResponse);\n}\n"
	writeProto := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, "proto", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeProto("services/widget/v1/widget.proto", proto)
	genDir := filepath.Join(dir, "gen")
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(d ForgeDescriptor) {
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(genDir, "forge_descriptor.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stamp := func() { write(ForgeDescriptor{Sources: DescriptorSources(dir)}) }
	reset := func() {
		writeProto("services/widget/v1/widget.proto", proto)
		_ = os.Remove(filepath.Join(dir, "proto", "shared", "v1", "types.proto"))
	}

	t.Run("matching stamp loads", func(t *testing.T) {
		reset()
		stamp()
		got, err := loadDescriptor(dir)
		if err != nil {
			t.Fatalf("a descriptor matching the protos must load: %v", err)
		}
		if got == nil {
			t.Fatal("expected a descriptor")
		}
	})

	for _, tc := range []struct {
		name, want string
		change     func()
	}{
		{"proto edited after extraction", "proto/services/widget/v1/widget.proto (edited)", func() {
			writeProto("services/widget/v1/widget.proto", proto+"\nmessage Extra {}\n")
		}},
		{"proto added after extraction", "proto/shared/v1/types.proto (added)", func() {
			writeProto("shared/v1/types.proto", "message Shared {}\n")
		}},
		{"proto deleted after extraction", "proto/services/widget/v1/widget.proto (deleted)", func() {
			if err := os.Remove(filepath.Join(protoDir, "widget.proto")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			reset()
			stamp()
			// The real-world sequence: change a proto, then run an inspection
			// command before regenerating.
			tc.change()
			_, err := loadDescriptor(dir)
			if err == nil {
				t.Fatal("a descriptor extracted from different protos must be REFUSED, not returned")
			}
			for _, want := range []string{"stale", "forge generate", tc.want} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal must say it is stale, name the proto and the fix; missing %q in: %v", want, err)
				}
			}
		})
	}

	t.Run("re-vendoring forge's annotation protos does not stale it", func(t *testing.T) {
		// forge.proto is the grammar the protos are written in, not an input
		// any descriptor is extracted from. Stamping it is what made every
		// forge.proto edit in forge's own repo restamp a descriptor with no
		// services and no configs.
		reset()
		writeProto("forge/v1/forge.proto", "syntax = \"proto3\";\npackage forge.v1;\n")
		writeProto("buf/validate/validate.proto", "syntax = \"proto2\";\npackage buf.validate;\n")
		stamp()
		writeProto("forge/v1/forge.proto", "syntax = \"proto3\";\npackage forge.v1;\nmessage NewOption {}\n")
		writeProto("buf/validate/validate.proto", "syntax = \"proto2\";\npackage buf.validate;\nmessage NewRule {}\n")
		if _, err := loadDescriptor(dir); err != nil {
			t.Fatalf("an edit to a vendored annotation proto must not stale the descriptor: %v", err)
		}
	})

	t.Run("unstamped file is allowed — only a stamped-then-drifted one is refused", func(t *testing.T) {
		// Deliberate scope limit. An unstamped descriptor is a hand-built
		// fixture with no proto tree behind it — several callers construct
		// one directly. Refusing those would break them for no safety gain:
		// the drift that actually happens is a descriptor stamped from protos
		// that then changed, and that IS refused (see the cases above).
		reset()
		writeProto("services/widget/v1/widget.proto", proto+"\n// drifted\n")
		write(ForgeDescriptor{})
		got, err := loadDescriptor(dir)
		if err != nil {
			t.Fatalf("an unstamped descriptor must still load: %v", err)
		}
		if got == nil {
			t.Fatal("expected a descriptor")
		}
	})

	t.Run("missing descriptor is still fine", func(t *testing.T) {
		if err := os.Remove(filepath.Join(genDir, "forge_descriptor.json")); err != nil {
			t.Fatal(err)
		}
		got, err := loadDescriptor(dir)
		if err != nil || got != nil {
			t.Fatalf("absent is a normal state (CLI/library projects); got %v, %v", got, err)
		}
	})
}

// The stamp must answer "was this extracted from THESE protos?" — not "have
// the files been touched?". A checkout, rebase or `cp -R` rewrites mtimes
// without changing meaning and must not invalidate a good descriptor.
func TestDescriptorSources_ContentNotMtime(t *testing.T) {
	mk := func(body string) string {
		dir := t.TempDir()
		pd := filepath.Join(dir, "proto", "svc", "v1")
		if err := os.MkdirAll(pd, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pd, "a.proto"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	a, b := mk("service S {}\n"), mk("service S {}\n")
	if DescriptorDrift(b, DescriptorSources(a)) != "" {
		t.Error("identical proto content in two trees must stamp identically — otherwise every clone reads as stale")
	}
	c := mk("service S { rpc X(Y) returns (Z); }\n")
	if DescriptorDrift(c, DescriptorSources(a)) == "" {
		t.Error("different proto content must stamp differently, or the guard cannot fire")
	}
	if got := DescriptorSources(t.TempDir()); got == nil || len(got) != 0 {
		t.Errorf("a project with no protos stamps an empty, non-nil set (a stamped descriptor with no inputs); got %#v", got)
	}
}

// One entry per proto, so a change to one proto changes only its own entry.
// That is what lets two branches that edit different protos merge their
// stamps into the stamp of the merged tree (see the merge driver's
// TestMergeGenerated_DescriptorSourcesMergeKeyByKey); a single digest of the
// tree changed on every branch and matched no merge.
func TestDescriptorSources_OneEntryPerProto(t *testing.T) {
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"a/v1/a.proto": "service A {}\n",
		"b/v1/b.proto": "service B {}\n",
	} {
		p := filepath.Join(dir, "proto", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := DescriptorSources(dir)
	if err := os.WriteFile(filepath.Join(dir, "proto", "a", "v1", "a.proto"), []byte("service A { rpc X(Y) returns (Z); }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after := DescriptorSources(dir)
	if before["a/v1/a.proto"] == after["a/v1/a.proto"] {
		t.Error("the edited proto's entry must change")
	}
	if before["b/v1/b.proto"] != after["b/v1/b.proto"] || len(after) != 2 {
		t.Errorf("only the edited proto's entry may change: before %v, after %v", before, after)
	}
}
