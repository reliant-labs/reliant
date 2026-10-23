package nodeengine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSatisfies(t *testing.T) {
	for _, tc := range []struct {
		version, rng string
		ok, judged   bool
	}{
		// The scaffolded frontends' floor (vite 8, vitest 5).
		{"v22.12.0", ">=22.12", true, true},
		{"v22.23.1", ">=22.12", true, true},
		{"v24.1.0", ">=22.12", true, true},
		{"v22.11.0", ">=22.12", false, true},
		{"v20.20.2", ">=22.12", false, true},
		// vite 8's own range, and vitest 5's.
		{"v20.19.0", "^20.19 || >=22.12", true, true},
		{"v20.18.1", "^20.19 || >=22.12", false, true},
		{"v21.7.0", "^20.19 || >=22.12", false, true},
		{"v25.0.0", "^22.12 || ^24 || >=26", false, true},
		{"v26.0.0", "^22.12 || ^24 || >=26", true, true},
		{"22.12.0", "22", true, true},
		// Forms it does not read are not judged, so never refused.
		{"v20.0.0", "<23", false, false},
		{"v20.0.0", ">=18 <21", false, false},
		{"v20.0.0", "22.x", false, false},
		{"garbage", ">=22", false, false},
	} {
		ok, judged := Satisfies(tc.version, tc.rng)
		if ok != tc.ok || judged != tc.judged {
			t.Errorf("Satisfies(%q, %q) = (%v, %v), want (%v, %v)", tc.version, tc.rng, ok, judged, tc.ok, tc.judged)
		}
	}
}

func withNode(t *testing.T, version string, err error) {
	t.Helper()
	prev := nodeVersion
	nodeVersion = func() (string, error) { return version, err }
	t.Cleanup(func() { nodeVersion = prev })
}

func frontendDir(t *testing.T, pkgJSON string) string {
	t.Helper()
	dir := t.TempDir()
	if pkgJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The CI repro: the scaffold's Next.js frontend on Node 20.
func TestCheck_RefusesANodeBelowTheFloor(t *testing.T) {
	withNode(t, "v20.20.2", nil)
	dir := frontendDir(t, `{"name":"admin","engines":{"node":">=22.12"}}`)
	err := Check(dir)
	if err == nil {
		t.Fatal("Node 20 under engines.node >=22.12 must be refused")
	}
	for _, want := range []string{">=22.12", "v20.20.2", "edgesOut", "nvm install 22"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestCheck_NilWhenItCannotJudge(t *testing.T) {
	withNode(t, "v20.20.2", nil)
	for name, pkg := range map[string]string{
		"no package.json":   "",
		"no engines":        `{"name":"x"}`,
		"unread range":      `{"engines":{"node":"<23"}}`,
		"invalid json":      `{`,
		"satisfying engine": `{"engines":{"node":">=20"}}`,
	} {
		if err := Check(frontendDir(t, pkg)); err != nil {
			t.Errorf("%s: Check = %v, want nil", name, err)
		}
	}
	withNode(t, "", errors.New("node: not found"))
	if err := Check(frontendDir(t, `{"engines":{"node":">=22.12"}}`)); err != nil {
		t.Errorf("no node on PATH: Check = %v, want nil (npm's own error says it)", err)
	}
}
