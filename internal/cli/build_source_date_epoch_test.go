package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/config"
)

func pinGitCommitTime(t *testing.T, fn func(ctx context.Context, dir string, inputs []string) (string, error)) {
	t.Helper()
	prev := gitInputsCommitTime
	gitInputsCommitTime = fn
	t.Cleanup(func() { gitInputsCommitTime = prev })
}

// The epoch is the commit time of the build's INPUTS, asked of git with exactly
// the context and Dockerfile — not HEAD's, which moves on every commit.
func TestResolveSourceDateEpoch_AsksGitAboutTheInputsOnly(t *testing.T) {
	t.Setenv(sourceDateEpochArg, "")
	var gotDir string
	var gotInputs []string
	pinGitCommitTime(t, func(_ context.Context, dir string, inputs []string) (string, error) {
		gotDir, gotInputs = dir, inputs
		return "1761350400", nil
	})
	got := resolveSourceDateEpoch(context.Background(), "/proj", "tools/otelcol-reliant", "", "tools/otelcol-reliant/Dockerfile")
	if got != "1761350400" {
		t.Fatalf("epoch = %q, want the git commit time", got)
	}
	if gotDir != "/proj" || !reflect.DeepEqual(gotInputs, []string{"tools/otelcol-reliant", "tools/otelcol-reliant/Dockerfile"}) {
		t.Fatalf("git asked in %q about %q; want /proj and the two non-empty inputs", gotDir, gotInputs)
	}
}

func TestResolveSourceDateEpoch_Precedence(t *testing.T) {
	pinGitCommitTime(t, func(context.Context, string, []string) (string, error) { return "200", nil })

	t.Run("environment wins over git", func(t *testing.T) {
		t.Setenv(sourceDateEpochArg, "100")
		if got := resolveSourceDateEpoch(context.Background(), "."); got != "100" {
			t.Fatalf("epoch = %q, want $SOURCE_DATE_EPOCH", got)
		}
	})
	t.Run("a non-numeric environment value is ignored", func(t *testing.T) {
		t.Setenv(sourceDateEpochArg, "yesterday")
		if got := resolveSourceDateEpoch(context.Background(), "."); got != "200" {
			t.Fatalf("epoch = %q, want git's", got)
		}
	})
	t.Run("no inputs means the whole tree", func(t *testing.T) {
		t.Setenv(sourceDateEpochArg, "")
		var gotInputs []string
		pinGitCommitTime(t, func(_ context.Context, _ string, inputs []string) (string, error) {
			gotInputs = inputs
			return "300", nil
		})
		if got := resolveSourceDateEpoch(context.Background(), "."); got != "300" || !reflect.DeepEqual(gotInputs, []string{"."}) {
			t.Fatalf("epoch = %q from inputs %q, want 300 from [.]", got, gotInputs)
		}
	})
}

// Outside a git checkout, or for inputs no commit touched, forge passes
// nothing: the Dockerfile's own default applies.
func TestResolveSourceDateEpoch_NothingWhenGitHasNoAnswer(t *testing.T) {
	t.Setenv(sourceDateEpochArg, "")
	for name, fn := range map[string]func(context.Context, string, []string) (string, error){
		"not a repository": func(context.Context, string, []string) (string, error) { return "", errors.New("not a git repository") },
		"untracked inputs": func(context.Context, string, []string) (string, error) { return "", nil },
	} {
		t.Run(name, func(t *testing.T) {
			pinGitCommitTime(t, fn)
			if got := resolveSourceDateEpoch(context.Background(), "."); got != "" {
				t.Fatalf("epoch = %q, want none", got)
			}
		})
	}
}

func TestAppendSourceDateEpoch(t *testing.T) {
	base := []string{"build", "-t", "img:1", "-f", "Dockerfile", "."}
	if got := appendSourceDateEpoch(base, "42"); !reflect.DeepEqual(got,
		[]string{"build", "--provenance=false", "--build-arg", "SOURCE_DATE_EPOCH=42", "-t", "img:1", "-f", "Dockerfile", "."}) {
		t.Fatalf("got %q; want both flags right after build", got)
	}
	if got := appendSourceDateEpoch(base, ""); !reflect.DeepEqual(got,
		[]string{"build", "--provenance=false", "-t", "img:1", "-f", "Dockerfile", "."}) {
		t.Fatalf("an empty epoch must still disable provenance, and add no build arg: %q", got)
	}
	// A DockerBuild's own build_args entry is the user's choice and wins; so
	// does an explicit --provenance / --attest.
	for _, explicit := range [][]string{
		{"build", "--provenance=mode=max", "--build-arg", "SOURCE_DATE_EPOCH=7", "."},
		{"build", "--attest", "type=provenance", "--build-arg=SOURCE_DATE_EPOCH=7", "."},
		{"build", "--provenance", "true", "--build-arg", "SOURCE_DATE_EPOCH", "."},
	} {
		if got := appendSourceDateEpoch(explicit, "42"); !reflect.DeepEqual(got, explicit) {
			t.Fatalf("overrode an explicit choice: %q", got)
		}
	}
	// A different arg whose name merely starts the same is not the same arg.
	other := []string{"build", "--build-arg", "SOURCE_DATE_EPOCH_X=1", "."}
	if got := appendSourceDateEpoch(other, "42"); countFlag(got, "SOURCE_DATE_EPOCH=42") != 1 {
		t.Fatalf("got %q; want the epoch added beside SOURCE_DATE_EPOCH_X", got)
	}
}

// A DockerBuild's build_args SOURCE_DATE_EPOCH survives the service argv
// assembly AND the epoch injection unchanged.
func TestServiceDockerBuild_ExplicitSourceDateEpochBuildArgWins(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "control-plane"}
	d := &DockerBuild{BuildArgs: map[string]string{"SOURCE_DATE_EPOCH": "9"}}
	args, _ := serviceDockerBuildArgs(cfg, "svc", "Dockerfile", d, buildOptions{}, "", "")
	args = appendSourceDateEpoch(args, "42")
	if !argsHave(args, "--build-arg", "SOURCE_DATE_EPOCH=9") || strings.Contains(strings.Join(args, " "), "SOURCE_DATE_EPOCH=42") {
		t.Fatalf("args = %q; want the declared 9 and no 42", args)
	}
}

// The real git lookup: the epoch is the last commit touching the inputs, and a
// later commit elsewhere does not move it.
func TestGitInputsCommitTime_IgnoresUnrelatedCommits(t *testing.T) {
	if testing.Short() {
		t.Skip("execs git to build a repository; runs in task test")
	}
	dir := t.TempDir()
	git := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	at := func(ts int64) []string {
		d := time.Unix(ts, 0).UTC().Format(time.RFC3339)
		return []string{"GIT_AUTHOR_DATE=" + d, "GIT_COMMITTER_DATE=" + d,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}
	}
	git(nil, "init", "-q")
	if err := os.MkdirAll(filepath.Join(dir, "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool", "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(at(1700000000), "add", ".")
	git(at(1700000000), "commit", "-q", "-m", "tool")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(at(1800000000), "add", ".")
	git(at(1800000000), "commit", "-q", "-m", "unrelated")

	got, err := gitInputsCommitTime(context.Background(), dir, []string{"tool", "tool/Dockerfile"})
	if err != nil || got != "1700000000" {
		t.Fatalf("epoch = %q, %v; want the tool commit's 1700000000", got, err)
	}
	if got, _ := gitInputsCommitTime(context.Background(), dir, []string{"."}); got != "1800000000" {
		t.Fatalf("whole-tree epoch = %q, want HEAD's 1800000000", got)
	}
}
