package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewEnvInvalidBindWritesNothing(t *testing.T) {
	root := writeProjectFixture(t, "prod")
	for _, force := range []bool{false, true} {
		dst := filepath.Join(root, "deploy/kcl/preview")
		if force {
			if err := os.MkdirAll(dst, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dst, "main.k"), []byte("original"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		withCwd(t, root, func() {
			cmd := newEnvNewCmd()
			args := []string{"preview", "--from", "prod", "--bind", "missing=hosted"}
			if force {
				args = append(args, "--force")
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatal("expected missing binding refusal")
			}
		})
		raw, err := os.ReadFile(filepath.Join(dst, "main.k"))
		if force {
			if err != nil || string(raw) != "original" {
				t.Fatalf("refusal overwrote destination: %s %v", raw, err)
			}
		} else if !os.IsNotExist(err) {
			t.Fatalf("refusal wrote destination: %v", err)
		}
	}
}

func TestProjectNewFrontendOptionsValidateBeforeWriting(t *testing.T) {
	for _, args := range [][]string{
		{"landing", "--output", "static"},
		{"landing", "--frontend", "web", "--output", "invalid"},
	} {
		root := t.TempDir()
		cmd := newNewCmd()
		cmd.SetArgs(append(args, "--path", root))
		if err := cmd.Execute(); err == nil {
			t.Fatal("expected invalid flags")
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("wrote on invalid flags: %v %v", entries, err)
		}
	}
	cmd := newNewCmd()
	for _, name := range []string{"output", "routes"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("project new lacks --%s", name)
		}
	}
	if err := cmd.ParseFlags([]string{"--frontend", "web", "--output", "static", "--routes", "none"}); err != nil {
		t.Fatal(err)
	}
}

func TestFrontendOnlyHasNoClusterPhase(t *testing.T) {
	e := &KCLEntities{Frontends: []FrontendEntity{{Name: "web", Path: "frontends/web"}}}
	required := envPhaseRequirements(e)
	if required.deploy || required.cluster {
		t.Fatalf("frontend-only requested infrastructure: %+v", required)
	}
	cmd := buildFrontendCmd(t.Context(), e.Frontends[0], "dev", nil, nil, "")
	if !strings.HasSuffix(cmd.Dir, "frontends/web") {
		t.Fatalf("starts wrong directory %q", cmd.Dir)
	}
}
