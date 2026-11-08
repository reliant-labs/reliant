//go:build integration

package templates

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/goexec"
)

// Build the rendered production stage verbatim, supplying a tiny probe in
// place of the app builder. The probe deliberately does NOT embed time/tzdata:
// it must get IANA zones from the shipped image, as ordinary app code does.
func TestDockerfile_ProductionLoadLocation(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a Docker image; runs in the integration lane")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skipf("requires a Docker daemon: %v", err)
	}
	dir := t.TempDir()
	probe := `package main
import (
    "fmt"
    "time"
)
func main() {
    for _, tc := range []struct { zone string; month time.Month; offset int }{
        {"America/New_York", time.January, -5*3600},
        {"America/New_York", time.July, -4*3600},
        {"Asia/Kathmandu", time.January, 5*3600+45*60},
    } {
        loc, err := time.LoadLocation(tc.zone)
        if err != nil { panic(err) }
        _, offset := time.Date(2026, tc.month, 15, 12, 0, 0, 0, time.UTC).In(loc).Zone()
        if offset != tc.offset { panic(fmt.Sprintf("%s: offset %d, want %d", tc.zone, offset, tc.offset)) }
    }
    fmt.Println("named zones and DST offsets verified")
}
`
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(probe), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", filepath.Join(dir, "demo"), filepath.Join(dir, "probe.go"))
	build.Env = goexec.WithTrimpath(append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOWORK=off", "GOFLAGS="))
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build timezone probe: %v\n%s", err, out)
	}
	src := string(renderProject(t, "Dockerfile.tmpl", projectData()))
	start := strings.Index(src, "FROM alpine:3.21 AS production\n")
	if start < 0 {
		t.Fatal("missing production stage")
	}
	dockerfile := "FROM scratch AS builder\nCOPY demo /app/bin/demo\n\n" + src[start:]
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	image := "forge-timezone-test:" + strings.ToLower(filepath.Base(filepath.Dir(dir))) + "-" + filepath.Base(dir)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if out, err := exec.CommandContext(cleanupCtx, "docker", "image", "rm", image).CombinedOutput(); err != nil {
			t.Logf("remove test image: %v\n%s", err, out)
		}
	})
	if out, err := exec.CommandContext(ctx, "docker", "build", "-t", image, dir).CombinedOutput(); err != nil {
		t.Fatalf("build production image: %v\n%s", err, out)
	}
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network=none", "--read-only", "--cap-drop=ALL", "-e", "GOROOT=/no-go-toolchain", "-e", "ZONEINFO=/no-zoneinfo-override", image).CombinedOutput()
	if err != nil {
		t.Fatalf("time.LoadLocation in production image: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "named zones and DST offsets verified") {
		t.Fatalf("probe did not complete: %s", out)
	}
}
