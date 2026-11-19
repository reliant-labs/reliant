// Copyright (c) 2025 Reliant Labs
package forgetest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pin = "v1.2.3-0.20260101000000-abcdefabcdef"

// The order scripts/forge.sh takes too (scripts/test-forge.sh checks the
// script): FORGE_BIN; a forge on PATH, then in .tools/bin, only at the pin;
// reliant forge; else nothing, and never a `go run`.
func TestResolveOrder(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		pin      string
		forgeBin string
		path     map[string]string // name on PATH -> its --version
		tools    string            // .tools/bin/forge's --version ("" = absent)
		want     string
		wantFrom string
		wantNone bool
	}{
		"FORGE_BIN wins":                 {pin: pin, forgeBin: "/given/forge", path: map[string]string{"forge": "forge version " + pin}, want: "/given/forge", wantFrom: "FORGE_BIN"},
		"forge on PATH at the pin":       {pin: pin, path: map[string]string{"forge": "forge version " + pin, "reliant": ""}, want: "/path/forge", wantFrom: "PATH"},
		"another forge falls to reliant": {pin: pin, path: map[string]string{"forge": "forge version v1.0.0", "reliant": ""}, want: "/path/reliant forge", wantFrom: "reliant"},
		".tools/bin at the pin":          {pin: pin, path: map[string]string{"forge": "forge version v1.0.0", "reliant": ""}, tools: "forge version " + pin + " (x)", want: "TOOLS", wantFrom: ".tools/bin"},
		".tools/bin at another version":  {pin: pin, path: map[string]string{"reliant": ""}, tools: "forge version v1.0.0", want: "/path/reliant forge", wantFrom: "reliant"},
		"reliant alone":                  {pin: pin, path: map[string]string{"reliant": ""}, want: "/path/reliant forge", wantFrom: "reliant"},
		"a dev pin takes any forge":      {pin: "dev", path: map[string]string{"forge": "forge version v1.0.0", "reliant": ""}, want: "/path/forge", wantFrom: "PATH"},
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
				require.NoError(t, os.MkdirAll(filepath.Dir(tools), 0o755))
				require.NoError(t, os.WriteFile(tools, []byte("#!/bin/sh\n"), 0o755))
			}
			versions := map[string]string{tools: tc.tools}
			for n, v := range tc.path {
				versions["/path/"+n] = v
			}
			env := Env{
				Dir:    sub,
				Getenv: func(k string) string { return map[string]string{"FORGE_BIN": tc.forgeBin}[k] },
				LookPath: func(n string) (string, error) {
					if _, ok := tc.path[n]; ok {
						return "/path/" + n, nil
					}
					return "", errors.New("not found")
				},
				Version: func(bin string) (string, error) { return versions[bin], nil },
			}
			got, err := Resolve(env)
			if tc.wantNone {
				require.ErrorIs(t, err, ErrNoForge)
				assert.Contains(t, err.Error(), pin)
				return
			}
			require.NoError(t, err)
			want := tc.want
			if want == "TOOLS" {
				want = tools
			}
			assert.Equal(t, want, got.String())
			assert.Equal(t, tc.wantFrom, got.Source)
		})
	}
}

func TestForgeCommandPutsForgeArgsAfterTheRoute(t *testing.T) {
	t.Parallel()
	f := Forge{Argv: []string{"/bin/reliant", "forge"}}
	cmd := f.Command(t.Context(), "obs", "check")
	assert.Equal(t, []string{"/bin/reliant", "forge", "obs", "check"}, cmd.Args)
	assert.Equal(t, []string{"/bin/reliant", "forge"}, f.Argv, "Command must not write into Argv")
}
