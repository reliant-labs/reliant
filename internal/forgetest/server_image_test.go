package forgetest

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

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
		})
	}
}
