package worktreereclaim

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// recordInstall writes the record a package manager leaves in the
// node_modules it populated: npm's hidden lockfile naming npmPaths (relative
// to node_modules' parent, "node_modules/pkg"), and, when pnpm is non-nil,
// pnpm's .modules.yaml and a .pnpm/lock.yaml whose snapshots are the keys in
// pnpm ("@scope/name@1.0.0").
func recordInstall(t *testing.T, nodeModules string, pnpm []string, npmPaths ...string) {
	t.Helper()
	packages := map[string]any{"": map[string]string{"name": "app"}}
	for _, p := range npmPaths {
		packages[p] = map[string]string{"version": "1.0.0"}
	}
	b, err := json.Marshal(map[string]any{"name": "app", "lockfileVersion": 3, "packages": packages})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(nodeModules, ".package-lock.json"), string(b))
	if pnpm != nil {
		lock := "lockfileVersion: '9.0'\n\nsnapshots:\n"
		for _, key := range pnpm {
			lock += "\n  '" + key + "': {}\n"
		}
		write(t, filepath.Join(nodeModules, ".modules.yaml"), "layoutVersion: 5\n")
		write(t, filepath.Join(nodeModules, ".pnpm", "lock.yaml"), lock)
	}
}
