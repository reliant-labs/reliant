package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestPlanKCLDockerRemote_ShowsTheWholeDockerCommand is bug 361c0e72: the line
// forge printed for a DockerBuild read `docker build -f Dockerfile . (2 tags)`,
// so the build args, the --platform, the target and the tags — what decides the
// image — were not in the log. The plan prints the same command line the build
// does (dockerCommandLine over serviceDockerBuildArgs), with every argument,
// and a secret-looking build arg withheld.
func TestPlanKCLDockerRemote_ShowsTheWholeDockerCommand(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A token-shaped value, assembled here so no credential-shaped literal
	// sits in the source (it was never a real token).
	token := "ghp_" + strings.Repeat("x0", 18)
	steps := planKCLDockerRemote(planInputs{
		cfg:        &config.ProjectConfig{Name: "p"},
		projectDir: root,
		entities: &KCLEntities{Workloads: []WorkloadEntity{{
			Name:  "gw",
			Image: "registry.example/prod/gw",
			Build: BuildConfigEntity{Type: "docker", Docker: &DockerBuild{
				Platform: "linux/arm64",
				Target:   "runtime",
				BuildArgs: map[string]string{
					"GO_VERSION":    "1.25",
					"NPM_TOKEN":     token,
					"APP_TITLE":     "My App",
					"LDFLAGS_EXTRA": "-X main.mode=prod",
				},
			}},
		}}},
		resolvedTag: "v1",
	})
	if len(steps) != 1 {
		t.Fatalf("expected one step, got %d: %+v", len(steps), steps)
	}
	got := steps[0].what
	for _, want := range []string{
		"docker build ",
		"--platform=linux/arm64",
		"--target runtime",
		"--build-arg GO_VERSION=1.25",
		"--build-arg 'APP_TITLE=My App'",
		"--build-arg 'LDFLAGS_EXTRA=-X main.mode=prod'",
		"--build-arg 'NPM_TOKEN=[redacted]'",
		"-t registry.example/prod/gw:v1",
		"-f Dockerfile .",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("printed docker command is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, token) {
		t.Errorf("printed docker command leaks a secret build arg value:\n%s", got)
	}
	if strings.Contains(got, "tags)") {
		t.Errorf("printed docker command still summarizes the tags instead of naming them:\n%s", got)
	}
}

func TestDockerCommandLine(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "plain arguments print unquoted, in order",
			args: []string{"build", "--platform=linux/amd64", "-t", "reg.example/a:v1", "-t", "a:latest", "-f", "Dockerfile", "."},
			want: "docker build --platform=linux/amd64 -t reg.example/a:v1 -t a:latest -f Dockerfile .",
		},
		{
			name: "an argument a shell would split or expand is single-quoted",
			args: []string{"build", "--build-arg", "MSG=it's $HOME", "--build-arg", "EMPTY=", "."},
			want: `docker build --build-arg 'MSG=it'\''s $HOME' --build-arg EMPTY= .`,
		},
		{
			name: "a secret build arg is withheld by name, in both spellings",
			args: []string{"build", "--build-arg", "DB_PASSWORD=hunter2", "--build-arg=API_KEY=abc", "."},
			want: "docker build --build-arg 'DB_PASSWORD=[redacted]' '--build-arg=API_KEY=[redacted]' .",
		},
		{
			name: "a secret build arg is withheld by shape whatever its name",
			args: []string{"build", "--build-arg", "REPO=https://bob:s3cr3t@git.example/x.git"},
			want: "docker build --build-arg 'REPO=[redacted]'",
		},
		{
			name: "a pass-through build arg has no value to withhold",
			args: []string{"build", "--build-arg", "NPM_TOKEN", "."},
			want: "docker build --build-arg NPM_TOKEN .",
		},
		{
			name: "a git commit and a version are shown",
			args: []string{"build", "--build-arg", "FORGE_COMMIT=0123456789abcdef0123456789abcdef01234567", "--build-arg", "FORGE_VERSION=v1.2.3"},
			want: "docker build --build-arg FORGE_COMMIT=0123456789abcdef0123456789abcdef01234567 --build-arg FORGE_VERSION=v1.2.3",
		},
		{
			name: "a build context URL keeps its address and loses its credential",
			args: []string{"build", "--build-context", "src=https://bob:s3cr3t@git.example/x.git#main", "--build-context", "tok=https://ghp_abc@github.com/o/r.git", "--build-context", "sib=../sibling"},
			want: "docker build --build-context 'src=https://bob:[redacted]@git.example/x.git#main' --build-context 'tok=https://[redacted]@github.com/o/r.git' --build-context sib=../sibling",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dockerCommandLine(c.args); got != c.want {
				t.Errorf("\n got %s\nwant %s", got, c.want)
			}
		})
	}
}

// TestBuildServiceDocker_PrintsTheCommandItRuns: the line a DockerBuild prints
// is the argv it hands docker — every argument — with only a secret value
// withheld. Before bug 361c0e72 it printed `docker build -f Dockerfile .
// (2 tags)`.
func TestBuildServiceDocker_PrintsTheCommandItRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("execs a fake docker and git; runs in task test")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("FORGE_STORAGE_POLICY", filepath.Join(t.TempDir(), "storage.json"))
	dockerLog := fakeDocker(t)

	const secret = "hunter2-not-for-logs"
	d := &DockerBuild{
		Platform:  "linux/arm64",
		BuildArgs: map[string]string{"GO_VERSION": "1.25", "DB_PASSWORD": secret},
	}
	var res buildResult
	out := captureStdout(t, func() {
		res = buildServiceDocker(context.Background(), &config.ProjectConfig{Name: "p"}, "gw", "gw", d, buildOptions{}, "", "v1")
	})
	if res.err != nil {
		t.Fatalf("buildServiceDocker: %v\n%s", res.err, out)
	}

	var ran string
	for _, line := range strings.Split(readTrim(t, dockerLog), "\n") {
		if strings.HasPrefix(line, "build ") {
			ran = line
		}
	}
	if ran == "" {
		t.Fatalf("docker build never ran; docker calls:\n%s", readTrim(t, dockerLog))
	}
	// The fake records argv space-joined; no argument here needs quoting, so
	// the printed line is exactly that argv with the secret withheld.
	want := "[build] gw: docker " + strings.Replace(ran, "DB_PASSWORD="+secret, "DB_PASSWORD=[redacted]", 1)
	want = strings.Replace(want, "DB_PASSWORD=[redacted]", "'DB_PASSWORD=[redacted]'", 1)
	if !strings.Contains(out, want+"\n") {
		t.Errorf("printed line is not the command that ran;\nwant %s\nout:\n%s", want, out)
	}
	for _, arg := range []string{"--platform=linux/arm64", "--build-arg GO_VERSION=1.25", "-t gw:v1"} {
		if !strings.Contains(ran, arg) {
			t.Errorf("docker did not receive %q: %s", arg, ran)
		}
	}
	if strings.Contains(out, secret) {
		t.Errorf("build output leaks the secret build arg value:\n%s", out)
	}
}
