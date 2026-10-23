package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/forgecompat"
)

// `forge project new --in-place` into a module whose go.mod already requires
// forge (new_inplace_forge_pin.go). Reproduced on reliant: forge.yaml got the
// RUNNING binary's v0.1.44-0.20261010080729-… over a go.mod pinning
// v0.1.44-0.20261007193810-e75c74afb46e.

const reliantForgePin = "v0.1.44-0.20261007193810-e75c74afb46e"

func writeGoModIn(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInspectAdoptedForge(t *testing.T) {
	for _, tc := range []struct {
		name    string
		goMod   string // "" = no go.mod
		wantOK  bool
		wantPin string
	}{
		{name: "no go.mod"},
		{name: "go.mod without forge", goMod: "module example.com/app\n\ngo 1.24\n\nrequire github.com/spf13/cobra v1.9.1\n"},
		{name: "pinned forge", goMod: "module github.com/reliant-labs/reliant\n\ngo 1.24\n\nrequire github.com/reliant-labs/forge " + reliantForgePin + "\n",
			wantOK: true, wantPin: reliantForgePin},
		// A replaced forge is adopted (it is the module's forge), but its
		// require is a placeholder generate does not follow, so forge.yaml
		// keeps recording the binary — exactly as the reconcile step would.
		{name: "replaced forge", goMod: "module example.com/app\n\ngo 1.24\n\nrequire github.com/reliant-labs/forge v0.1.40\n\nreplace github.com/reliant-labs/forge => ../forge\n",
			wantOK: true, wantPin: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.goMod != "" {
				writeGoModIn(t, dir, tc.goMod)
			}
			// Library kind: no toolchain question to ask.
			a, ok := inspectAdoptedForge(dir, config.ProjectKindLibrary)
			if ok != tc.wantOK || a.pin != tc.wantPin {
				t.Fatalf("inspectAdoptedForge = (%+v, %v), want pin %q ok %v", a, ok, tc.wantPin, tc.wantOK)
			}
			if a.known {
				t.Error("a library scaffold asked the toolchain how forge resolves; nothing it generates imports forge")
			}
		})
	}
}

// The generator side: an adopted pin is what forge.yaml records and what a
// (--force-)rendered go.mod requires.
func TestWriteScaffoldFiles_InPlaceRecordsAdoptedForgePin(t *testing.T) {
	target := t.TempDir()
	userGoMod := "module example.com/adopted\n\ngo 1.24\n\nrequire github.com/reliant-labs/forge " + reliantForgePin + "\n"
	writeGoModIn(t, target, userGoMod)

	a, ok := inspectAdoptedForge(target, config.ProjectKindLibrary)
	if !ok {
		t.Fatal("a go.mod requiring forge was not adopted")
	}
	captureStdout(t, func() {
		if _, err := writeScaffoldFiles(scaffoldFilesInput{
			gen: newGeneratorConfig{
				projectName: "adopted",
				targetPath:  target,
				modulePath:  "example.com/adopted",
				kind:        config.ProjectKindLibrary,
				binary:      config.ProjectBinaryPerService,
				harness:     "reliant",
				forgePin:    a.pin,
			},
			bufPlugins: "local",
			inPlace:    true,
		}); err != nil {
			t.Fatalf("writeScaffoldFiles: %v", err)
		}
	})

	data, err := os.ReadFile(filepath.Join(target, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "forge_version: "+reliantForgePin) {
		t.Errorf("forge.yaml does not record the pin go.mod already declares (%s):\n%s", reliantForgePin, data)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "go.mod")); string(got) != userGoMod {
		t.Errorf("the user's go.mod was changed:\n%s", got)
	}
}

// adoptedForgeResolution is forgecompat.Decide's verdict on the module's own
// resolution, said before anything is written.
func TestAdoptedForgeResolution(t *testing.T) {
	t.Setenv(forgecompat.SkewOverrideEnv, "")
	const raw = "v0.1.45-0.20261011230828-7555f0177ef8"
	resolved := func(v string) adoptedForge { return adoptedForge{pin: v, resolved: v, known: true} }
	for _, tc := range []struct {
		name           string
		kind           string
		a              adoptedForge
		linked         bool
		binary, raw    string
		linkable       string
		want           []string // substrings; nil = allowed
		wantNotContain string
	}{
		{name: "toolchain could not answer", kind: config.ProjectKindService, a: adoptedForge{pin: reliantForgePin}, raw: raw},
		{name: "library imports no forge", kind: config.ProjectKindLibrary, a: resolved(reliantForgePin), raw: raw},
		{name: "unreleased but linked", kind: config.ProjectKindService, a: resolved(reliantForgePin), linked: true, raw: raw, linkable: "/src/forge"},
		{name: "unreleased, module already resolves locally", kind: config.ProjectKindService, a: adoptedForge{known: true, local: true}, raw: raw},
		// The module resolves exactly the commit this binary was built from:
		// the proxy demonstrably serves it.
		{name: "unreleased, pin is this very build", kind: config.ProjectKindService, a: resolved(raw), raw: raw},
		{name: "same release", kind: config.ProjectKindService, a: resolved("v0.1.45"), binary: "v0.1.45", raw: "v0.1.45"},
		{
			name: "unreleased, not linked", kind: config.ProjectKindService, a: resolved(reliantForgePin), raw: raw, linkable: "/src/forge",
			want: []string{"unreleased build", reliantForgePin, "Nothing was written", "will not move it",
				"go run github.com/reliant-labs/forge/cmd/forge@" + reliantForgePin, "--link-forge", "/src/forge"},
		},
		{
			name: "unreleased, no checkout to link", kind: config.ProjectKindService, a: resolved(reliantForgePin), raw: raw,
			want:           []string{"go run github.com/reliant-labs/forge/cmd/forge@" + reliantForgePin},
			wantNotContain: "--link-forge",
		},
		{
			name: "pin older than this release", kind: config.ProjectKindService, a: resolved("v0.1.44"), binary: "v0.1.45", raw: "v0.1.45",
			want: []string{"older than the forge generating into it", "Nothing was written",
				"go run github.com/reliant-labs/forge/cmd/forge@v0.1.44", "go get github.com/reliant-labs/forge@v0.1.45"},
		},
		{
			name: "this release older than the pin", kind: config.ProjectKindService, a: resolved("v0.1.45"), binary: "v0.1.44", raw: "v0.1.44",
			want: []string{"OLDER than", "Nothing was written", "go run github.com/reliant-labs/forge/cmd/forge@v0.1.45",
				forgecompat.SkewOverrideEnv},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := adoptedForgeResolution(tc.kind, tc.a, tc.linked, tc.binary, tc.raw, tc.linkable,
				[]string{"project", "new", "--in-place"})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("refused a pairing generate accepts: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("allowed a pairing generate would refuse")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not mention %q:\n%s", want, err)
				}
			}
			if tc.wantNotContain != "" && strings.Contains(err.Error(), tc.wantNotContain) {
				t.Errorf("refusal offers %q, which this binary cannot honour:\n%s", tc.wantNotContain, err)
			}
		})
	}
}

// The real command path: a refusal leaves the user's directory exactly as it
// was — no forge.yaml, no .forge/ marker.
func TestRunNew_InPlaceRefusesPinItCannotGenerateIntoBeforeWriting(t *testing.T) {
	buildinfo.Set("v0.1.45", "", "unknown") // a stamped release: installable
	t.Cleanup(func() { buildinfo.Set("dev", "", "unknown") })
	prev := resolveAdoptedForge
	resolveAdoptedForge = func(string) (string, bool, bool) { return "v0.1.44", false, true }
	t.Cleanup(func() { resolveAdoptedForge = prev })

	target := t.TempDir()
	writeGoModIn(t, target, "module example.com/adopted\n\ngo 1.24\n\nrequire github.com/reliant-labs/forge v0.1.44\n")

	err := runNew(t.Context(), "adopted", target, "example.com/adopted", config.ProjectKindService,
		nil, nil, "", true, false, nil, "", true, "local", "", false, false, false, false, newFrontendOptions{})
	if err == nil || !strings.Contains(err.Error(), "older than the forge generating into it") {
		t.Fatalf("runNew = %v, want the stale-pin refusal", err)
	}
	entries, _ := os.ReadDir(target)
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the refusal left files behind: %v (want only go.mod)", names)
	}
}

// goModForgeRequire used ParseLax, which drops `replace` (a main-module-only
// directive), so its "a replaced forge has no version to follow" branch never
// fired and generate converged forge.yaml onto a placeholder require.
func TestReconcileForgePin_ReplacedForgeLeavesPin(t *testing.T) {
	const forgeYAML = "name: app\nmodule_path: example.com/app\nforge_version: v0.1.45\n"
	dir, ctx := writePinProject(t, forgeYAML,
		"module example.com/app\n\ngo 1.24\n\nrequire github.com/reliant-labs/forge v0.1.40\n\nreplace github.com/reliant-labs/forge => ../forge\n")
	captureStdout(t, func() {
		if err := stepReconcileForgePin(ctx); err != nil {
			t.Fatalf("stepReconcileForgePin: %v", err)
		}
	})
	if got, _ := os.ReadFile(filepath.Join(dir, "forge.yaml")); string(got) != forgeYAML {
		t.Errorf("forge.yaml followed the require of a REPLACED forge:\n%s", got)
	}
}
