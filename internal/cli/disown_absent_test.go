package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// `forge project disown --absent` is the declared way to decline a scaffold
// — what reliant had to do by hand-writing .forge/scaffolded.json for
// serve.go, metrics.go and internal/metrics/metrics.go after every generate
// that scaffolded them rolled them back with their birth records.
func TestDisownAbsent_DeclinesAbsentScaffolds(t *testing.T) {
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)
	root := withDisownProjectRoot(t, nil)

	var out strings.Builder
	paths := []string{"cmd/reliant/cmd/serve.go", "./cmd/reliant/cmd/metrics.go", "internal/metrics/metrics.go"}
	if err := declineScaffolds(&out, root, paths, "serve.go turns composition on; roots arrive in R3", false); err != nil {
		t.Fatalf("decline: %v\n%s", err, out.String())
	}
	checksums.ResetScaffoldLedgerCache() // read what was persisted, not the cache
	for _, p := range []string{"cmd/reliant/cmd/serve.go", "cmd/reliant/cmd/metrics.go", "internal/metrics/metrics.go"} {
		why, ok := checksums.ScaffoldDeclined(root, p)
		if !ok || why != "serve.go turns composition on; roots arrive in R3" {
			t.Errorf("%s: declined=%v reason=%q", p, ok, why)
		}
		if checksums.ScaffoldOnceDecision(root, p) {
			t.Errorf("%s: a declined path would still be scaffolded", p)
		}
	}
	if !strings.Contains(out.String(), "rescaffold") {
		t.Errorf("output does not name the way back:\n%s", out.String())
	}

	// Idempotent: the same decline again changes nothing.
	out.Reset()
	if err := declineScaffolds(&out, root, paths[:1], "serve.go turns composition on; roots arrive in R3", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already declined") {
		t.Errorf("a repeated decline was not reported as a no-op:\n%s", out.String())
	}
}

// --absent refuses what it cannot honestly decline, before writing anything.
func TestDisownAbsent_Refusals(t *testing.T) {
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)
	root := withDisownProjectRoot(t, nil)
	writeFile := func(rel, content string) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("present.go", "package x\n")
	if _, err := checksums.WriteGeneratedFileTier1(root, "gen.go", []byte("package x\n"), &checksums.FileChecksums{}, false); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		paths []string
		want  string
	}{
		"present":   {[]string{"present.go"}, "on disk"},
		"forge":     {[]string{".forge/scaffolded.json"}, "not a project file"},
		"escape":    {[]string{"../elsewhere.go"}, "not a project file"},
		"together":  {[]string{"absent.go", "present.go"}, "on disk"},
		"generated": {[]string{"gen.go"}, "forge-generated"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			err := declineScaffolds(&out, root, tc.paths, "why", false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
			if _, ok := checksums.ScaffoldDeclined(root, "absent.go"); ok {
				t.Fatalf("a refused batch half-applied: absent.go was declined")
			}
		})
	}

	if err := runDisownAbsent(&strings.Builder{}, []string{"absent.go"}, "  ", false); err == nil {
		t.Fatalf("an empty --reason was accepted")
	}
}

// A failed generate names the scaffolds it un-created and the command that
// declines them — the moment the user needs it, not a skill away.
func TestRollbackReport_NamesUnbornScaffoldsAndTheDecline(t *testing.T) {
	var sb strings.Builder
	writeRollbackReport(&sb, rollbackReport{
		Restored: []string{"cmd/app/cmd/serve.go"},
		StepErr:  errors.New("compose: 104 missing providers"),
		Unborn:   []string{"cmd/app/cmd/serve.go"},
	})
	got := sb.String()
	for _, want := range []string{"cmd/app/cmd/serve.go", "project disown cmd/app/cmd/serve.go --absent --reason"} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
}
