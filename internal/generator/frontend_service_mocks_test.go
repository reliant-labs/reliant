package generator

import (
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var updateServiceMockGoldens = flag.Bool("update-service-mocks", false, "rewrite testdata/service_mocks/*.golden from the current render")

// sampleGenDir is the sample frontend's generated TypeScript, shared with
// web-runtime's own tests of the values these modules produce: real
// protoc-gen-es output for a non-CRUD service (nested messages, enums, maps,
// oneofs, well-known types, streams, protovalidate rules) and a vendored
// package whose TelemetryService collides with it by short name.
const sampleGenDir = "../../web-runtime/src/testdata/mockgen/gen"

func copySampleTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), body, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

// TestServiceDefaults_Golden emits the mock surface of a frontend whose
// src/gen holds the sample modules and pins what it writes: one
// src/mocks/services/<service>_gen.ts per service (qualified by package where
// two share a name), and a transport that gathers them.
func TestServiceDefaults_Golden(t *testing.T) {
	root := t.TempDir()
	const feRel = "frontends/web"
	copySampleTree(t, sampleGenDir, filepath.Join(root, feRel, "src", "gen"))
	cs, err := LoadChecksums(root)
	if err != nil {
		t.Fatalf("load ownership state: %v", err)
	}
	if _, err := EmitFrontendMockSurface(root, feRel, nil, nil, nil, cs); err != nil {
		t.Fatalf("emit mock surface: %v", err)
	}

	servicesDir := filepath.Join(root, feRel, "src", "mocks", "services")
	entries, err := os.ReadDir(servicesDir)
	if err != nil {
		t.Fatalf("read %s: %v", servicesDir, err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	want := []string{
		"acme-v1-telemetry-service_gen.ts",
		"billing-service_gen.ts",
		"sample-v1-telemetry-service_gen.ts",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("service modules = %v, want %v", got, want)
	}

	files := map[string]string{"mock-transport_gen.ts.golden": filepath.Join(root, feRel, "src", "lib", "mock-transport_gen.ts")}
	for _, name := range want {
		files[name+".golden"] = filepath.Join(servicesDir, name)
	}
	for golden, path := range files {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		goldenPath := filepath.Join("testdata", "service_mocks", golden)
		if *updateServiceMockGoldens {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goldenPath, body, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		wantBody, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s (run with -update-service-mocks to create): %v", goldenPath, err)
		}
		if string(body) != string(wantBody) {
			t.Errorf("%s differs from %s (run with -update-service-mocks if intended):\n--- got ---\n%s", path, goldenPath, body)
		}
	}
}

// TestServiceDefaults_NoStubsNoRegistry: before the frontend's stubs exist
// (the scaffold-time call) no service module is written, and the transport
// names no services directory — a webpack context over a missing directory
// fails the build.
func TestServiceDefaults_NoStubsNoRegistry(t *testing.T) {
	root := t.TempDir()
	const feRel = "frontends/web"
	cs, err := LoadChecksums(root)
	if err != nil {
		t.Fatalf("load ownership state: %v", err)
	}
	if _, err := EmitFrontendMockSurface(root, feRel, nil, nil, nil, cs); err != nil {
		t.Fatalf("emit mock surface: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, feRel, "src", "mocks", "services")); !os.IsNotExist(err) {
		t.Errorf("src/mocks/services exists without any generated stubs (err=%v)", err)
	}
	body, err := os.ReadFile(filepath.Join(root, feRel, "src", "lib", "mock-transport_gen.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"../mocks/services", "isServiceDefaults", "services,"} {
		if strings.Contains(string(body), s) {
			t.Errorf("transport without services mentions %q", s)
		}
	}
}
