package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/generate-timings.sh is how the merge gate reports generate's five
// slowest steps. forge prints its FORGE_GENERATE_TIMINGS table on stderr, so
// the stub generator below prints it there too, with stdout noise around it:
// a capture of stdout alone reported nothing (reliant #2385, review B1).
const stubGenerator = `#!/usr/bin/env bash
[ "$FORGE_GENERATE_TIMINGS" = 1 ] || { echo "FORGE_GENERATE_TIMINGS not set" >&2; exit 3; }
echo "🔨 Validating generated code..."
cat >&2 <<'EOF'
⏱  generate step timings (71 step(s) ran, 1m8.49s total):
      10.4s   15.2%  generate hooks (forge.yaml)
      9.19s   13.4%  check stale scaffold tests
      6.76s    9.9%  internal package contracts
      6.07s    8.9%  go build (validate generated code)
      5.78s    8.4%  internal/app composition (hybrid DI)
      5.13s    7.5%  generate input hooks (forge.yaml)
EOF
echo "done"
exit "${STUB_EXIT:-0}"
`

func TestGenerateTimingsReportsTheFiveSlowestStepsFromStderr(t *testing.T) {
	root := scriptFixture(t, "generate-timings.sh")
	putFixture(t, root, "gen", stubGenerator, 0755)
	notes := filepath.Join(root, "notes")

	out, err := runFixture(root, "generate-timings.sh", nil, notes, filepath.Join(root, "gen"))
	if err != nil {
		t.Fatalf("a passing generator failed the step: %v\n%s", err, out)
	}
	for _, want := range []string{"Validating generated code", "generate step timings", "done"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the generator's output lost %q (the log must keep all of it):\n%s", want, out)
		}
	}
	got, err := os.ReadFile(notes)
	if err != nil {
		t.Fatal(err)
	}
	want := "generate slowest steps: 10.4s 15.2% generate hooks (forge.yaml); 9.19s 13.4% check stale scaffold tests; " +
		"6.76s 9.9% internal package contracts; 6.07s 8.9% go build (validate generated code); 5.78s 8.4% internal/app composition (hybrid DI)\n"
	if string(got) != want {
		t.Errorf("notes =\n%q\nwant\n%q", got, want)
	}
}

func TestGenerateTimingsKeepsTheGeneratorsExitStatus(t *testing.T) {
	root := scriptFixture(t, "generate-timings.sh")
	putFixture(t, root, "gen", stubGenerator, 0755)
	notes := filepath.Join(root, "notes")

	_, err := runFixture(root, "generate-timings.sh", []string{"STUB_EXIT=7"}, notes, filepath.Join(root, "gen"))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("err = %v, want the generator's own exit status 7 (a failed generate must fail the phase)", err)
	}
	if b, _ := os.ReadFile(notes); !strings.Contains(string(b), "generate slowest steps: 10.4s") {
		t.Errorf("a failed generate still reports its timings; notes = %q", b)
	}
}
