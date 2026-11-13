package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// One env, one arch. With no platform declared anywhere (no
// ClusterTarget.platform, no deploy.target_arch, no --target-arch), every
// image lane of a build must target the HOST arch, as ClusterTarget.platform
// documents ("unset => host arch") and as the project image and ShellBuilds
// (resolveBuildArchForImage) already did. A per-service DockerBuild and a
// docker frontend used to default to amd64 instead, so an arm64 host built an
// arm64 project image beside amd64 service images (forge bug report
// 1087dbe4). A declared platform, and a DockerBuild's own platform, still win.
func TestImageLanesAgreeOnTheArchWhenNoneIsDeclared(t *testing.T) {
	for _, host := range []string{"arm64", "amd64"} {
		t.Run("host="+host, func(t *testing.T) {
			withHostArch(t, host)
			cfg := &config.ProjectConfig{Name: "acme"}

			if got := resolveBuildArchForImage("", ""); got != host {
				t.Fatalf("project image / ShellBuild arch = %q, want the host %q", got, host)
			}
			// The per-service DockerBuild: no --platform means docker builds
			// for its (host) daemon — the same arch as the project image.
			args, _ := serviceDockerBuildArgs(cfg, "svc", "Dockerfile", &DockerBuild{}, buildOptions{}, "", "tag")
			if p := platformArg(args); p != "" {
				t.Errorf("per-service DockerBuild on a %s host with no declared platform passed --platform=%s; want none (host arch, as the project image)", host, p)
			}
			// The docker-frontend lane resolves through the same helper.
			if a := resolveBuildArch("", "", true); a != "" {
				t.Errorf("docker frontend arch on a %s host with no declared platform = %q, want \"\" (host)", host, a)
			}
		})
	}

	t.Run("declared platform still wins", func(t *testing.T) {
		withHostArch(t, "arm64")
		cfg := &config.ProjectConfig{Name: "acme"}
		args, _ := serviceDockerBuildArgs(cfg, "svc", "Dockerfile", &DockerBuild{}, buildOptions{}, "amd64", "tag")
		if p := platformArg(args); p != "linux/amd64" {
			t.Errorf("env platform amd64 on an arm64 host: --platform = %q, want linux/amd64", p)
		}
		args, _ = serviceDockerBuildArgs(cfg, "svc", "Dockerfile", &DockerBuild{Platform: "linux/s390x"}, buildOptions{}, "amd64", "tag")
		if p := platformArg(args); p != "linux/s390x" {
			t.Errorf("an explicit DockerBuild.platform must win: --platform = %q", p)
		}
		args, _ = serviceDockerBuildArgs(cfg, "svc", "Dockerfile", &DockerBuild{}, buildOptions{targetArch: "amd64"}, "", "tag")
		if p := platformArg(args); p != "linux/amd64" {
			t.Errorf("--target-arch amd64 on an arm64 host: --platform = %q, want linux/amd64", p)
		}
	})
}

// platformArg returns the value of a docker `--platform=<p>` argument, or "".
func platformArg(args []string) string {
	const flag = "--platform"
	for _, a := range args {
		if len(a) > len(flag)+1 && a[:len(flag)+1] == flag+"=" {
			return a[len(flag)+1:]
		}
	}
	return ""
}
