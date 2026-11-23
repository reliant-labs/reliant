package forgetest

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseServerImageIsBuildOnlyAndReceivesVersionStamp(t *testing.T) {
	forge := Pinned(t) // Use the same KCL evaluator as env build, in the full lane.
	t.Setenv("FORGE_LEDGER", "machine")
	t.Setenv("FORGE_LEDGER_HOME", t.TempDir())
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	type workload struct {
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Image   string `json:"image"`
		Runtime struct {
			Type string `json:"type"`
		} `json:"runtime"`
		Build struct {
			Type       string            `json:"type"`
			Dockerfile string            `json:"dockerfile"`
			BuildArgs  map[string]string `json:"build_args"`
			Inputs     []string          `json:"inputs"`
		} `json:"build"`
	}
	type bundle struct {
		Workloads []workload        `json:"workloads"`
		Manifests []json.RawMessage `json:"manifests"`
		Frontends []json.RawMessage `json:"frontends"`
	}
	for _, tc := range []struct {
		name string
		opts []string
		want map[string]string
	}{
		{name: "dockerfile defaults", want: map[string]string{
			"VERSION": "dev", "COMMIT": "unknown", "BUILD_DATE": "unknown", "BRANCH": "unknown", "COMMIT_TIME": "0",
		}},
		{name: "explicit release stamp", opts: []string{
			"-D", "stamp_version=v1.42.0", "-D", "stamp_commit=0123456789abcdef0123456789abcdef01234567", "-D", "stamp_date=2026-11-20T00:00:00Z",
			"-D", "stamp_branch=release/test", "-D", "stamp_commit_time=1795132800",
		}, want: map[string]string{
			"VERSION": "v1.42.0", "COMMIT": "0123456789abcdef0123456789abcdef01234567", "BUILD_DATE": "2026-11-20T00:00:00Z", "BRANCH": "release/test", "COMMIT_TIME": "1795132800",
		}},
		// The release recipe passes no stamp_date: BUILD_DATE is the commit
		// time, so the build args are a function of the commit and a second
		// release of it reuses the first one's image.
		{name: "build date from the commit time", opts: []string{
			"-D", "stamp_version=v1.42.0", "-D", "stamp_commit=0123456789abcdef0123456789abcdef01234567",
			"-D", "stamp_branch=release/test", "-D", "stamp_commit_time=1709164809",
		}, want: map[string]string{
			"VERSION": "v1.42.0", "COMMIT": "0123456789abcdef0123456789abcdef01234567", "BUILD_DATE": "2024-02-29T00:00:09Z", "BRANCH": "release/test", "COMMIT_TIME": "1709164809",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"kcl", "eval", "deploy/kcl/release/main.k", "-S", "output"}, tc.opts...)
			cmd := forge.Command(t.Context(), args...)
			cmd.Dir = root
			out, err := cmd.Output()
			require.NoError(t, err, "render release: %s", out)
			var got bundle
			require.NoError(t, json.Unmarshal(out, &got))
			assert.Empty(t, got.Manifests, "the release environment must not deploy objects")
			assert.Empty(t, got.Frontends)
			// The release env declares other build-only artifacts beside the
			// image (the CLI variants); this test owns only the image.
			i := slices.IndexFunc(got.Workloads, func(w workload) bool {
				return w.Name == "reliant-image"
			})
			require.GreaterOrEqual(t, i, 0, "the release environment must declare reliant-image")
			image := got.Workloads[i]
			assert.Equal(t, "tool", image.Kind)
			assert.Equal(t, "build-only", image.Runtime.Type)
			assert.Equal(t, "us-central1-docker.pkg.dev/reliant-nonprod-490701/reliant-nonprod/reliant", image.Image)
			assert.Equal(t, "docker", image.Build.Type)
			assert.Equal(t, "Dockerfile", image.Build.Dockerfile)
			assert.Equal(t, tc.want, image.Build.BuildArgs)
			// Declared inputs are what lets a release reuse an earlier
			// release's image; the Go module files must be among them.
			assert.Subset(t, image.Build.Inputs, []string{"Dockerfile", "go.mod", "go.sum", "cmd/**", "internal/**"})
			if tc.name == "dockerfile defaults" {
				assertServerImageDependencyClosure(t, root, image.Build.Inputs)
			}
		})
	}
}

// Resolve the actual Docker targets, not the host's dependency graph. This runs
// in the full lane (Pinned skips under -short): go list's child-process reads
// cannot be tracked by Go's test cache. External module contents are pinned by
// go.mod/go.sum; all in-context sources, local replacements and embeds must be
// covered by the rendered DockerBuild inputs.
func assertServerImageDependencyClosure(t *testing.T, root string, inputs []string) {
	t.Helper()
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run("dependency closure linux/"+arch, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-json", "./cmd/reliant/")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GOWORK=off", "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0", "GOFLAGS=")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			require.NoError(t, err, "Docker-target dependency graph: %s", stderr.String())
			decoder := json.NewDecoder(bytes.NewReader(out))
			localPackages, embeddedFiles := 0, 0
			for {
				var pkg struct {
					Dir, ImportPath                                                                                                     string
					GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles, SwigCXXFiles, SysoFiles, EmbedFiles []string
				}
				err := decoder.Decode(&pkg)
				if err == io.EOF {
					break
				}
				require.NoError(t, err)
				rel, err := filepath.Rel(root, pkg.Dir)
				require.NoError(t, err)
				if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					continue // Standard library or a versioned module outside the build context.
				}
				localPackages++
				embeddedFiles += len(pkg.EmbedFiles)
				for _, files := range [][]string{pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.MFiles, pkg.HFiles, pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles, pkg.SysoFiles, pkg.EmbedFiles} {
					for _, file := range files {
						path := filepath.ToSlash(filepath.Join(rel, file))
						included := false
						for _, pattern := range inputs {
							negative := strings.HasPrefix(pattern, "!")
							matched, err := doublestar.Match(strings.TrimPrefix(pattern, "!"), path)
							require.NoError(t, err)
							if matched {
								included = !negative
							}
						}
						assert.True(t, included, "%s input %s missing from server_image_inputs", pkg.ImportPath, path)
					}
				}
			}
			require.Positive(t, localPackages, "must inspect local packages")
			require.Positive(t, embeddedFiles, "must inspect embedded assets too")
			t.Logf("linux/%s: checked %d local packages and %d embedded files", arch, localPackages, embeddedFiles)
		})
	}
}
