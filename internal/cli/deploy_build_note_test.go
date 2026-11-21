package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The release-override note fires only when the recorded build is NEWER than
// the release: a build state from before the release was cut is not "just
// built", and a release cut after it already accounts for it.
func TestReleaseOverrideNoteOnlyForANewerBuild(t *testing.T) {
	testkit.Slow(t, "execs git")
	cut := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		pushedAt string
		wantNote bool
	}{
		"build before the release": {cut.Add(-time.Hour).Format(time.RFC3339), false},
		"build after the release":  {cut.Add(time.Hour).Format(time.RFC3339), true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := testCutRelease(t, dir, release.Release{
				Version: "v1.0.0", CreatedAt: cut,
				Artifacts: map[string]release.Artifact{
					"reg.example.com/app": {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: sha("a")}},
				},
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := testBindings(t, dir).Append(context.Background(), release.Promotion{
				Env: "prod", Release: "v1.0.0", Kind: release.KindPromote,
				Resolved: map[string]string{"reg.example.com/app": sha("a")},
			}, appendGuard{}); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(dir, ".forge", "state", "build-prod.json")
			if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(state, []byte(`{"image": "reg.example.com/app", "tag": "t", "pushed": true, "pushed_at": "`+tc.pushedAt+`", "digest": "`+sha("b")+`"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			out := captureStdout(t, func() {
				if _, _, err := resolveDeployDigests(context.Background(), dir, "prod", false, testBindings(t, dir), testReleases(t, dir)); err != nil {
					t.Fatal(err)
				}
			})
			if got := strings.Contains(out, "deploying the RELEASE"); got != tc.wantNote {
				t.Fatalf("note printed = %v, want %v:\n%s", got, tc.wantNote, out)
			}
		})
	}
}
