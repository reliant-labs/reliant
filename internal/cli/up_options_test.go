package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRenderOptions(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		want    []string
		wantErr string
	}{
		{
			name:   "no options bind nothing",
			values: nil,
			want:   nil,
		},
		{
			name:   "value is wrapped in a KCL string literal",
			values: []string{"host_runner=go-run"},
			want:   []string{`host_runner="go-run"`},
		},
		{
			name:   "bindings are sorted so repeat renders are byte-identical",
			values: []string{"zeta=1", "alpha=2"},
			want:   []string{`alpha="2"`, `zeta="1"`},
		},
		{
			// Quoting is what keeps forge out of type-guessing: unquoted, KCL
			// would parse this as an int and the project's `str` option would
			// silently receive the wrong type.
			name:   "numeric value stays a string",
			values: []string{"port=8080"},
			want:   []string{`port="8080"`},
		},
		{
			name:   "value may contain = and spaces",
			values: []string{"flags=--a=1 --b=2"},
			want:   []string{`flags="--a=1 --b=2"`},
		},
		{
			name:   "empty value is legal",
			values: []string{"thing="},
			want:   []string{`thing=""`},
		},
		{
			name:    "missing = is rejected",
			values:  []string{"host_runner"},
			wantErr: "expected name=value",
		},
		{
			name:    "missing name is rejected",
			values:  []string{"=go-run"},
			wantErr: "missing option name",
		},
		{
			name:    "forge-derived names are refused with the reason",
			values:  []string{"image_tag=nope"},
			wantErr: "is set by forge, not by you",
		},
		{
			name:    "same option twice is a conflict, not last-wins",
			values:  []string{"a=1", "a=2"},
			wantErr: "given twice",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRenderOptions(tt.values)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("parseRenderOptions(%q) = nil error, want %q", tt.values, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRenderOptions(%q): %v", tt.values, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("got %q, want %q", got, tt.want)
				}
			}
		})
	}
}

// Every reserved name must be refused — the list is forge's contract for
// "options you don't get to set", and a gap in it is a silent override of a
// derived value.
func TestParseRenderOptionsRefusesEveryReservedName(t *testing.T) {
	for _, name := range []string{
		"env", "namespace", "image_tag", "image_digests", "worktree", "branch",
		"frontend_outputs",
	} {
		if _, err := parseRenderOptions([]string{name + "=x"}); err == nil {
			t.Errorf("-D %s=x was accepted; it is derived by forge", name)
		}
	}
}

// `registry` is NOT forge's: forge declares no option of that name and binds
// none. A project whose own main.k reads `option("registry")` gets `-D
// registry=…` relayed like any other project option — neither refused as
// reserved nor given any other special treatment.
func TestParseRenderOptionsRelaysAProjectRegistryOption(t *testing.T) {
	got, err := parseRenderOptions([]string{"registry=ghcr.io/acme"})
	if err != nil {
		t.Fatalf("-D registry=ghcr.io/acme was refused: %v — a project-declared option must be relayed", err)
	}
	if len(got) != 1 || got[0] != `registry="ghcr.io/acme"` {
		t.Errorf("parseRenderOptions = %q, want the value relayed verbatim", got)
	}
}

// The full CLI plumbing: a -D value must arrive at the env's KCL as
// option("<name>"). Renders a fixture through the real render path rather than
// asserting on the dArgs slice, so a break anywhere in flag → parse → global →
// dArgs → kcl is caught.
func TestRenderOptionsReachTheKCL(t *testing.T) {
	t.Cleanup(func() { setRenderOptions(nil) })
	// The fixture short-circuit would bypass the render entirely.
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", "")

	projectDir := t.TempDir()
	envDir := filepath.Join(projectDir, "deploy", "kcl", "dev")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No imports: this test is about the binding reaching KCL, not about the
	// entity contract, so it stays independent of the forge module.
	main := `_runner = option("host_runner") or "air"
out = {runner = _runner}
`
	if err := os.WriteFile(filepath.Join(envDir, "main.k"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}

	render := func(t *testing.T, values []string) string {
		t.Helper()
		dArgs, err := parseRenderOptions(values)
		if err != nil {
			t.Fatalf("parseRenderOptions(%q): %v", values, err)
		}
		setRenderOptions(dArgs)
		raw, err := renderKCLRaw(context.Background(), projectDir, "dev")
		if err != nil {
			t.Fatalf("renderKCLRaw: %v", err)
		}
		return string(raw)
	}

	t.Run("unset falls through to the KCL default", func(t *testing.T) {
		if got := render(t, nil); !strings.Contains(got, `"runner": "air"`) {
			t.Errorf("render = %s, want the declared default", got)
		}
	})

	t.Run("-D reaches option()", func(t *testing.T) {
		got := render(t, []string{"host_runner=go-run"})
		if !strings.Contains(got, `"runner": "go-run"`) {
			t.Errorf("render = %s, want the -D value", got)
		}
	})

	t.Run("clearing the options restores the default render", func(t *testing.T) {
		setRenderOptions(nil)
		raw, err := renderKCLRaw(context.Background(), projectDir, "dev")
		if err != nil {
			t.Fatalf("renderKCLRaw: %v", err)
		}
		if !strings.Contains(string(raw), `"runner": "air"`) {
			t.Errorf("render = %s, want the default back", raw)
		}
	})
}

// A project that can't be introspected must not have its -D rejected: the
// render is about to run and will report the real problem with better context.
func TestValidateRenderOptionsDegradesPermissively(t *testing.T) {
	projectDir := t.TempDir()
	envDir := filepath.Join(projectDir, "deploy", "kcl", "dev")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "main.k"), []byte("this is not valid kcl {{{\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateRenderOptions(projectDir, "dev", []string{"anything=1"}); err != nil {
		t.Errorf("validateRenderOptions on an unparseable env = %v, want nil (permissive)", err)
	}
}

// No -D means no work: validation must not parse the project at all.
func TestValidateRenderOptionsNoopWithoutFlags(t *testing.T) {
	if err := validateRenderOptions("/nonexistent-project", "dev", nil); err != nil {
		t.Errorf("validateRenderOptions with no -D = %v, want nil", err)
	}
}

// `forge build -D` must reject a -D given without the environment argument.
//
// Options are declared per-env (deploy/kcl/<env>/), so without an env there is
// nothing to validate the name against and nothing to render — the value would
// be silently dropped. Failing loudly is the point: the whole reason this flag
// exists on `build` is that the alternatives silently did the wrong thing
// (`forge env up` builds every workload; `build -t` had no way to pass a value).
func TestBuildRenderOptionsRequireEnv(t *testing.T) {
	t.Cleanup(func() { setRenderOptions(nil) })

	err := runBuild(context.Background(), buildOptions{
		renderOptions: []string{"desktop_channel=local"},
		// env deliberately empty
	})
	if err == nil {
		t.Fatal("runBuild with -D and no env: want error, got nil")
	}
	if !strings.Contains(err.Error(), "-D requires the environment argument") {
		t.Fatalf("runBuild with -D and no env: unexpected error %q", err)
	}
}

// `forge env up -D` must survive the delegation into the build phase.
//
// env up binds the render options itself and then calls runBuild. runBuild also
// binds -D (its own flag), and binding an EMPTY set there published nil over
// what the orchestrator had already set — so the build phase re-rendered with
// no options and built the wrong variant. Observed: `forge env up prod -D
// desktop_channel=local` ran the RELEASE packaging target and died on missing
// Apple notarization credentials, instead of the local one.
func TestBuildDoesNotClobberOrchestratorRenderOptions(t *testing.T) {
	t.Cleanup(func() { setRenderOptions(nil) })

	// What `forge env up` does before delegating.
	dArgs, err := parseRenderOptions([]string{"desktop_channel=local"})
	if err != nil {
		t.Fatalf("parseRenderOptions: %v", err)
	}
	setRenderOptions(dArgs)

	// runBuild with NO -D of its own must leave that binding intact. Reaching
	// the full build is not the point — the guard runs before any of it, so an
	// error from later stages is fine.
	_ = runBuild(context.Background(), buildOptions{env: "prod", buildTarget: "all"})

	got := activeRenderOptionDArgs()
	if len(got) != 1 || !strings.Contains(got[0], "desktop_channel") {
		t.Fatalf("runBuild clobbered the orchestrator's render options: got %v, want the desktop_channel binding", got)
	}
}
