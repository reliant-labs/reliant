package cli

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// `forge project new demo-app` run from the PARENT directory renders the
// frontend config with a relative project dir. The probe path was built by
// joining that dir in, then handed to a renderer that resolves it against the
// same dir again — so KCL was asked for demo-app/demo-app/deploy/kcl/dev/…,
// the read failed, and config.js silently fell back to proto defaults.
//
// Not parallel: it changes the working directory, which is what makes the
// project dir relative in the first place.
func TestLoadFrontendRuntimeConfig_RelativeProjectDir(t *testing.T) {
	projectDir := identityProject(t)
	fc := identityFrontendConfig()

	want, err := loadFrontendRuntimeConfig(projectDir, "dev", []codegen.FrontendConfig{fc})
	if err != nil {
		t.Fatalf("absolute project dir: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("fixture is wrong: the dev env must project at least one frontend value")
	}

	t.Chdir(filepath.Dir(projectDir))
	got, err := loadFrontendRuntimeConfig(filepath.Base(projectDir), "dev", []codegen.FrontendConfig{fc})
	if err != nil {
		t.Fatalf("a relative project dir must render the same env as an absolute one: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("relative project dir rendered different values:\n got: %v\nwant: %v", got, want)
	}
}
