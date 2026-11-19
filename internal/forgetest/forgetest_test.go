// Copyright (c) 2025 Reliant Labs
package forgetest

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/testkit"
)

const pin = "v1.2.3-0.20260101000000-abcdefabcdef"

// The order scripts/forge.sh takes too (scripts/test-forge.sh checks the
// script): FORGE_BIN; a forge on PATH, then in .tools/bin, only at the pin;
// the pin through reliant; else nothing, and never a `go run`.
func TestResolveOrder(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		pin       string
		forgeBin  string
		path      map[string]string // name on PATH -> its --version
		tools     string            // .tools/bin/forge's --version ("" = absent)
		cached    string            // the pinned build reliant keeps reports this ("" = absent)
		probeErr  bool              // reliant cannot obtain the pin
		want      string
		wantFrom  string
		wantProbe bool
		wantNone  bool
	}{
		"FORGE_BIN wins":                 {pin: pin, forgeBin: "/given/forge", path: map[string]string{"forge": "forge version " + pin}, want: "/given/forge", wantFrom: "FORGE_BIN"},
		"forge on PATH at the pin":       {pin: pin, path: map[string]string{"forge": "forge version " + pin, "reliant": ""}, want: "/path/forge", wantFrom: "PATH"},
		".tools/bin at the pin":          {pin: pin, path: map[string]string{"forge": "forge version v1.0.0", "reliant": ""}, tools: "forge version " + pin + " (x)", want: "TOOLS", wantFrom: ".tools/bin"},
		"reliant embeds the pin":         {pin: pin, path: map[string]string{"forge": "forge version v1.0.0", "reliant": "forge version " + pin + " (embedded in reliant)"}, want: "/path/reliant forge", wantFrom: "reliant"},
		"reliant keeps a build of it":    {pin: pin, path: map[string]string{"reliant": "forge version v1.0.0 (embedded in reliant)"}, tools: "forge version v1.0.0", cached: "forge version " + pin, want: "CACHED", wantFrom: "reliant's pinned build", wantProbe: true},
		"reliant's build is another pin": {pin: pin, path: map[string]string{"reliant": "forge version v1.0.0"}, cached: "forge version v1.0.1", wantProbe: true, wantNone: true},
		"reliant cannot obtain the pin":  {pin: pin, path: map[string]string{"reliant": "forge version v1.0.0"}, probeErr: true, wantProbe: true, wantNone: true},
		"a dev pin takes any forge":      {pin: "dev", path: map[string]string{"forge": "forge version v1.0.0", "reliant": ""}, want: "/path/forge", wantFrom: "PATH"},
		"a dev pin takes reliant forge":  {pin: "dev", path: map[string]string{"reliant": "forge version v1.0.0"}, want: "/path/reliant forge", wantFrom: "reliant"},
		"a +dirty pin takes any forge":   {pin: pin + "+dirty", path: map[string]string{"forge": "forge version v1.0.0"}, want: "/path/forge", wantFrom: "PATH"},
		"nothing: no go run":             {pin: pin, path: map[string]string{"go": "", "forge": "forge version v1.0.0"}, wantNone: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "forge.yaml"),
				[]byte("name: x\nforge_version: \""+tc.pin+"\" # the pin\n"), 0o644))
			sub := filepath.Join(root, "internal", "pkg")
			require.NoError(t, os.MkdirAll(sub, 0o755))
			tools := filepath.Join(root, ".tools", "bin", "forge")
			if tc.tools != "" {
				writeExec(t, tools, "#!/bin/sh\n")
			}
			cache := t.TempDir()
			cached := filepath.Join(cache, tc.pin, "linux-amd64", "forge")
			if tc.cached != "" {
				writeExec(t, cached, "#!/bin/sh\n")
				// A build of another pin beside it is never taken.
				writeExec(t, filepath.Join(cache, "v1.0.0", "linux-amd64", "forge"), "#!/bin/sh\n")
			}
			versions := map[string]string{tools: tc.tools, cached: tc.cached}
			for n, v := range tc.path {
				versions["/path/"+n] = v
			}
			var probed string
			env := Env{
				Dir: sub,
				Getenv: func(k string) string {
					return map[string]string{"FORGE_BIN": tc.forgeBin, "FORGE_PINNED_CACHE": cache}[k]
				},
				LookPath: func(n string) (string, error) {
					if _, ok := tc.path[n]; ok {
						return "/path/" + n, nil
					}
					return "", errors.New("not found")
				},
				Version: func(argv ...string) (string, error) {
					if len(argv) == 2 && argv[0] == "/path/reliant" && argv[1] == "forge" {
						return versions["/path/reliant"], nil
					}
					return versions[argv[0]], nil
				},
				Run: func(dir string, argv ...string) error {
					probed = dir + ": " + strings.Join(argv, " ")
					if tc.probeErr {
						return errors.New("offline")
					}
					return nil
				},
				UserCacheDir: func() (string, error) { return "", errors.New("unused: FORGE_PINNED_CACHE is set") },
			}
			got, err := Resolve(env)
			if tc.wantProbe {
				assert.Equal(t, root+": /path/reliant forge lint --help", probed, "the hand-off probe runs in the project root")
			} else {
				assert.Empty(t, probed)
			}
			if tc.wantNone {
				require.ErrorIs(t, err, ErrNoForge)
				assert.Contains(t, err.Error(), tc.pin)
				return
			}
			require.NoError(t, err)
			want := map[string]string{"TOOLS": tools, "CACHED": cached}[tc.want]
			if want == "" {
				want = tc.want
			}
			assert.Equal(t, want, got.String())
			assert.Equal(t, tc.wantFrom, got.Source)
		})
	}
}

// The regression for running the forge outside the project: a reliant whose
// built-in forge is another version runs the pin only inside the project.
// The Forge Resolve returns, run from an unrelated directory, must still be
// the pin, not the forge built into reliant.
func TestResolvedForgeRunsThePinOutsideTheProject(t *testing.T) {
	testkit.Slow(t, "execs fake reliant and forge shell scripts")
	t.Parallel()
	root, cache, bin := t.TempDir(), t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("forge_version: "+pin+"\n"), 0o644))
	// The fake reliant behaves like its hand-off: inside a project (a
	// forge.yaml in the working directory) it builds the pin into the cache
	// (FORGE_PINNED_CACHE, which Getenv reports) once and runs it; anywhere
	// else it runs its built-in forge.
	writeExec(t, filepath.Join(bin, "reliant"), `#!/bin/sh
shift # forge
if [ -f forge.yaml ]; then
  b="`+cache+`/`+pin+`/linux-amd64/forge"
  mkdir -p "$(dirname "$b")"
  printf '#!/bin/sh\necho "forge version `+pin+`"\n' >"$b"
  chmod +x "$b"
  exec "$b" "$@"
fi
echo "forge version v0.0.1-embedded"
`)
	m := Machine(root)
	m.Getenv = func(k string) string { return map[string]string{"FORGE_PINNED_CACHE": cache}[k] }
	m.LookPath = func(n string) (string, error) {
		if n == "reliant" {
			return filepath.Join(bin, "reliant"), nil
		}
		return "", exec.ErrNotFound
	}

	// Run as it was before: `reliant forge` outside the project is the wrong forge.
	naive := exec.Command(filepath.Join(bin, "reliant"), "forge", "obs")
	naive.Dir = t.TempDir()
	out, err := naive.Output()
	require.NoError(t, err)
	require.Contains(t, string(out), "v0.0.1-embedded", "the fake must model reliant's built-in forge outside a project")

	f, err := Resolve(m)
	require.NoError(t, err)
	assert.Equal(t, "reliant's pinned build", f.Source)
	cmd := f.Command(t.Context(), "obs")
	cmd.Dir = t.TempDir()
	out, err = cmd.Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "forge version "+pin, "the forge a test runs outside the project is the pin")
}

func TestForgeCommandPutsForgeArgsAfterTheRoute(t *testing.T) {
	t.Parallel()
	f := Forge{Argv: []string{"/bin/reliant", "forge"}}
	cmd := f.Command(t.Context(), "obs", "check")
	assert.Equal(t, []string{"/bin/reliant", "forge", "obs", "check"}, cmd.Args)
	assert.Equal(t, []string{"/bin/reliant", "forge"}, f.Argv, "Command must not write into Argv")
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
}
