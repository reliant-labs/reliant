package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The merge gate's pgtest phase runs the gate's own text — its phase function
// and everything from the pgtest phase through the wait for the launched
// phases — with every tool it calls faked on PATH, so no Go, git or database
// is touched. It reads scripts/merge-gate.sh itself, so Go's test cache keys
// on it.
func gatePgtestRegion(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "merge-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	cut := func(start, end string) string {
		t.Helper()
		_, rest, ok := strings.Cut(src, start)
		if !ok {
			t.Fatalf("scripts/merge-gate.sh has no %q", start)
		}
		body, _, ok := strings.Cut(rest, end)
		if !ok {
			t.Fatalf("scripts/merge-gate.sh has no %q after %q", end, start)
		}
		return start + body + end
	}
	return cut("phase() {", "\n}\n") +
		cut("# ── pgtest: the shared test Postgres serves", `for p in ${pids[@]+"${pids[@]}"}; do wait "$p" || fail=1; done`)
}

func TestMergeGatePgtestPhase(t *testing.T) {
	region := gatePgtestRegion(t)
	for _, tc := range []struct {
		name string
		// The selection load_selection leaves.
		fgen, hooksOnly, gofull, typecheck string
		ngo                                int
		probeFails                         bool
		wantProbe                          bool
		// Phases that must and must not have been launched.
		want, notWant []string
	}{
		{name: "a failed probe starts no phase", fgen: "true", ngo: 2, probeFails: true, wantProbe: true,
			want: []string{"pgtest"}, notWant: []string{"generate", "build", "test"}},
		{name: "a passing probe lets every phase run", fgen: "true", ngo: 2, wantProbe: true,
			want: []string{"pgtest", "generate", "build", "test"}},
		{name: "Go tests alone need it", ngo: 1, wantProbe: true, want: []string{"pgtest", "build", "test"}},
		{name: "a full Go lane needs it", gofull: "true", wantProbe: true, want: []string{"pgtest", "build", "test"}},
		{name: "full generation alone needs it", fgen: "true", wantProbe: true, want: []string{"pgtest", "generate"}},
		{name: "hooks-only generation does not", fgen: "true", hooksOnly: "true",
			want: []string{"generate", "build"}, notWant: []string{"pgtest"}},
		{name: "a frontend-only change does not", typecheck: "true",
			want: []string{"typecheck", "build"}, notWant: []string{"pgtest"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each case is its own bash run over its own directory.
			t.Parallel()
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			logs := filepath.Join(dir, "logs")
			probed := filepath.Join(dir, "probed")
			for _, d := range []string{bin, logs} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			fake := func(name, body string) {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			// go: the probe answers as the case says; every other go is a pass.
			fake("go", `if [ "$1 $2" = "run ./tools/pgtest-check" ]; then
  echo "$*" >"$PROBED"
  [ "$PROBE_FAILS" = 1 ] && { echo "pgtest-check: FAILED: connection refused"; exit 1; }
  echo "pgtest-check: served"
fi
exit 0
`)
			fake("git", "exit 0\n") // a clean tree: generators run in scratch checkouts
			fake("jq", "exit 0\n")
			if err := os.WriteFile(filepath.Join(dir, "plan.json"), []byte(`{"go":{"packages":[],"build":[]}}`), 0o644); err != nil {
				t.Fatal(err)
			}
			probeFails := "0"
			if tc.probeFails {
				probeFails = "1"
			}
			script := `
fail=0 amend=0 queue=1 files= base=origin/main quarter=1 half=1 gate_cpus=1 t0=$(date +%s)
fgen=` + orDefault(tc.fgen, "false") + ` hooks_only=` + orDefault(tc.hooksOnly, "false") + ` gofull=` + orDefault(tc.gofull, "false") + `
typecheck=` + orDefault(tc.typecheck, "false") + ` ngo=` + strconv.Itoa(tc.ngo) + ` dgen=false webfull=false
notes=() forge=(forge)
scratch() { SCRATCH=` + dir + `; return 0; }
gen_forge() { return 0; }
` + region + `
echo "fail=$fail"
printf 'note: %s\n' "${notes[@]}"
`
			cmd := exec.Command("bash", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "L="+logs, "run_plan="+filepath.Join(dir, "plan.json"),
				"PROBED="+probed, "PROBE_FAILS="+probeFails)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("the gate's region did not run: %v\n%s", err, out)
			}
			b, _ := os.ReadFile(filepath.Join(logs, "expected"))
			launched := strings.Fields(string(b))
			for _, p := range tc.want {
				if !slices.Contains(launched, p) {
					t.Errorf("phase %s was not launched; launched %v\n%s", p, launched, out)
				}
			}
			for _, p := range tc.notWant {
				if slices.Contains(launched, p) {
					t.Errorf("phase %s was launched; launched %v\n%s", p, launched, out)
				}
			}
			_, err = os.Stat(probed)
			if probedNow := err == nil; probedNow != tc.wantProbe {
				t.Errorf("probe ran: %v, want %v\n%s", probedNow, tc.wantProbe, out)
			}
			if tc.probeFails {
				for _, want := range []string{"fail=1", "note: pgtest: the shared test Postgres could not serve a database", "pgtest-check: FAILED"} {
					if !strings.Contains(string(out), want) {
						t.Errorf("a failed probe must report %q:\n%s", want, out)
					}
				}
			}
		})
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
