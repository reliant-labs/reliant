package cli

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// A variant's declared env lands after forge's defaults, so os/exec (which
// keeps the last value of a duplicated key) lets it override CGO_ENABLED=0,
// and it is appended in a stable order.
func TestVariantBuildEnv_DeclaredEnvWinsInStableOrder(t *testing.T) {
	base := []string{"PATH=/bin", "CGO_ENABLED=0"}
	v := BuildVariant{
		Name: "darwin", GOOS: "darwin", GOARCH: "arm64",
		EnvAtBuild: map[string]string{"GOFLAGS": "-trimpath", "CGO_ENABLED": "1"},
	}
	got := variantBuildEnv(base, v)
	want := []string{"PATH=/bin", "CGO_ENABLED=0", "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=1", "GOFLAGS=-trimpath"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("env = %q\nwant %q", got, want)
	}
	if base[1] != "CGO_ENABLED=0" || len(base) != 2 {
		t.Errorf("base mutated: %q", base)
	}
}

func TestVariantOutputName(t *testing.T) {
	if got := variantOutputName("cli", BuildVariant{Name: "linux"}); got != "cli-linux" {
		t.Errorf("default = %q, want cli-linux", got)
	}
	if got := variantOutputName("cli", BuildVariant{Name: "linux", OutputName: "acme"}); got != "acme" {
		t.Errorf("override = %q, want acme", got)
	}
}

func TestVariantBinaryFilename(t *testing.T) {
	for _, tc := range []struct {
		variant    BuildVariant
		host, want string
	}{
		{BuildVariant{Name: "native"}, "windows", "cli-native.exe"},
		{BuildVariant{Name: "native"}, "linux", "cli-native"},
		{BuildVariant{GOOS: "windows", OutputName: "cli.EXE"}, "linux", "cli.EXE"},
		{BuildVariant{GOOS: "linux", OutputName: "cli.exe"}, "windows", "cli.exe"},
	} {
		if got := variantBinaryFilename("cli", tc.variant, tc.host); got != tc.want {
			t.Errorf("filename(%+v, %s) = %q, want %q", tc.variant, tc.host, got, tc.want)
		}
	}
}

func TestBuildOnlyVariantsRejectFilenameCollisionBeforeBuild(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{{
		Name: "tool",
		Runtime: RuntimeEntity{Type: RuntimeBuildOnly, BuildOnly: &BuildOnlyDeploy{BuildVariants: []BuildVariant{
			{Name: "windows", GOOS: "windows"},
			{Name: "linux", GOOS: "linux", OutputName: "tool-windows.exe"},
		}}},
	}}}
	got := buildKCLBuildOnlyVariants(context.Background(), e, t.TempDir())
	if len(got) != 1 || got[0].err == nil || !strings.Contains(got[0].err.Error(), `tool:windows and tool:linux write the same binary filename "tool-windows.exe"`) {
		t.Fatalf("want the effective filename collision, got %+v", got)
	}
}
