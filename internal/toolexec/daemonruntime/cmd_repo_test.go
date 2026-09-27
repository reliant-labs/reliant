// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func discoverForTest(t *testing.T, root string) repoDiscoverResponse {
	t.Helper()
	payload, err := json.Marshal(repoDiscoverRequest{Path: root})
	require.NoError(t, err)
	out, err := handleRepoDiscover(context.Background(), payload)
	require.NoError(t, err)
	var resp repoDiscoverResponse
	require.NoError(t, json.Unmarshal(out, &resp))
	return resp
}

// The forge project name is the key the web joins a Reliant project to its
// control-plane environments on, so discovery must carry it — and a forge.yaml
// forge itself would reject must never cost the project its repo discovery.
func TestRepoDiscoverReportsForgeProjectName(t *testing.T) {
	cases := []struct {
		name      string
		forgeYAML *string
		wantForge bool
		wantName  string
	}{
		{name: "no forge.yaml", forgeYAML: nil, wantForge: false, wantName: ""},
		{name: "named", forgeYAML: ptr("# manifest\nname: hounders\nmodule_path: github.com/x/y\n"), wantForge: true, wantName: "hounders"},
		{name: "name is trimmed", forgeYAML: ptr("name: \"  barksocial  \"\n"), wantForge: true, wantName: "barksocial"},
		{name: "no name key", forgeYAML: ptr("module_path: github.com/x/y\n"), wantForge: true, wantName: ""},
		{name: "unparseable", forgeYAML: ptr("name: [unterminated\n\t:::"), wantForge: true, wantName: ""},
		{name: "nested name is not the project name", forgeYAML: ptr("frontends:\n  - name: web\n"), wantForge: true, wantName: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.forgeYAML != nil {
				require.NoError(t, os.WriteFile(filepath.Join(root, "forge.yaml"), []byte(*tc.forgeYAML), 0o644))
			}
			resp := discoverForTest(t, root)
			assert.Equal(t, tc.wantForge, resp.HasForge)
			assert.Equal(t, tc.wantName, resp.ForgeProjectName)
		})
	}
}

func ptr(s string) *string { return &s }
