package forgeconv

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestLintAllocatePortSpacing_FullSet reproduces the real control-plane
// deploy/kcl/dev/main.k allocate_port base set verbatim (see the
// fixture at testdata/allocate_port_spacing/full_set) and pins the three
// load-bearing cases the incident report called out:
//
//   - 28090 (gateway controller) vs 29190 (gateway grpc) are both ≡ 90
//     mod 100, 11 blocks apart — NOT reachable at the project's default
//     max_stacks=8, but it becomes reachable the moment max_stacks is
//     raised past 11. This is the exact "invisible until you raise the
//     ceiling" defect the rule exists to catch, so the table below
//     asserts both sides of that line explicitly.
//   - 3000 (reliant-web) vs 3091 (reliant grpc) are NOT congruent mod
//     100 (0 vs 91) and must never fire, at any max_stacks — the
//     false-positive guard.
//   - 8090 (admin-server) vs 28090 (gateway controller) are both ≡ 90
//     mod 100 but 200 blocks apart — must NOT fire at max_stacks=8, but
//     MUST fire once max_stacks is raised past 200.
func TestLintAllocatePortSpacing_FullSet(t *testing.T) {
	t.Parallel()
	root := filepath.Join("testdata", "allocate_port_spacing", "full_set")

	t.Run("28090/29190 do not collide at the real default max_stacks=8", func(t *testing.T) {
		t.Parallel()
		res, err := LintAllocatePortSpacing(root, config.DefaultMaxStacks, config.DefaultBlockSize)
		if err != nil {
			t.Fatalf("LintAllocatePortSpacing: %v", err)
		}
		for _, f := range res.Findings {
			if strings.Contains(f.Message, "allocate_port(28090)") && strings.Contains(f.Message, "allocate_port(29190)") {
				t.Fatalf("28090/29190 must not fire at max_stacks=8 (only 11 blocks apart, "+
					"but ceiling is 8) — this is the invisibility the incident describes; "+
					"finding:\n%s", f.Message)
			}
		}
	})

	t.Run("28090/29190 FIRE once max_stacks is raised past 11 — block 11, port 29190", func(t *testing.T) {
		t.Parallel()
		res, err := LintAllocatePortSpacing(root, 12, config.DefaultBlockSize)
		if err != nil {
			t.Fatalf("LintAllocatePortSpacing: %v", err)
		}
		got := findingsForRule(res.Findings, "forgeconv-allocate-port-spacing")
		var hit *Finding
		for i := range got {
			if strings.Contains(got[i].Message, "allocate_port(28090)") && strings.Contains(got[i].Message, "allocate_port(29190)") {
				hit = &got[i]
				break
			}
		}
		if hit == nil {
			t.Fatalf("expected the 28090/29190 pair to fire at max_stacks=12; findings:\n%s", res.FormatText())
		}
		if hit.Severity != SeverityError {
			t.Errorf("severity = %s, want error — this is an exact arithmetic collision, not a heuristic", hit.Severity)
		}
		if !strings.Contains(hit.Message, "block 11") {
			t.Errorf("message must name the exact colliding block (11); got: %s", hit.Message)
		}
		if !strings.Contains(hit.Message, "29190") {
			t.Errorf("message must name the resulting shared port (29190); got: %s", hit.Message)
		}
		if !strings.Contains(hit.Message, "main.k") {
			t.Errorf("message must carry file:line for both sites; got: %s", hit.Message)
		}
	})

	t.Run("3000/3091 never fire — not congruent mod 100 (false-positive guard)", func(t *testing.T) {
		t.Parallel()
		// A very high ceiling so if the congruence check were broken
		// (e.g. comparing raw difference instead of mod-remainder)
		// this pair would light up.
		res, err := LintAllocatePortSpacing(root, 1000, config.DefaultBlockSize)
		if err != nil {
			t.Fatalf("LintAllocatePortSpacing: %v", err)
		}
		for _, f := range res.Findings {
			if strings.Contains(f.Message, "allocate_port(3000)") && strings.Contains(f.Message, "allocate_port(3091)") {
				t.Fatalf("3000 (≡0 mod 100) and 3091 (≡91 mod 100) are NOT congruent and must "+
					"never fire; finding:\n%s", f.Message)
			}
		}
	})

	t.Run("8090/28090 do not fire at max_stacks=8 (200 blocks apart)", func(t *testing.T) {
		t.Parallel()
		res, err := LintAllocatePortSpacing(root, config.DefaultMaxStacks, config.DefaultBlockSize)
		if err != nil {
			t.Fatalf("LintAllocatePortSpacing: %v", err)
		}
		for _, f := range res.Findings {
			if strings.Contains(f.Message, "allocate_port(8090)") && strings.Contains(f.Message, "allocate_port(28090)") {
				t.Fatalf("8090/28090 are 200 blocks apart — unreachable at max_stacks=8; finding:\n%s", f.Message)
			}
		}
	})

	t.Run("8090/28090 FIRE once max_stacks is raised past 200", func(t *testing.T) {
		t.Parallel()
		res, err := LintAllocatePortSpacing(root, 201, config.DefaultBlockSize)
		if err != nil {
			t.Fatalf("LintAllocatePortSpacing: %v", err)
		}
		got := findingsForRule(res.Findings, "forgeconv-allocate-port-spacing")
		found := false
		for _, f := range got {
			if strings.Contains(f.Message, "allocate_port(8090)") && strings.Contains(f.Message, "allocate_port(28090)") {
				found = true
				if !strings.Contains(f.Message, "block 200") {
					t.Errorf("message must name the exact colliding block (200); got: %s", f.Message)
				}
			}
		}
		if !found {
			t.Fatalf("expected the 8090/28090 pair to fire at max_stacks=201; findings:\n%s", res.FormatText())
		}
	})

	t.Run("comment mentioning allocate_port(28090,...) is not double-counted", func(t *testing.T) {
		t.Parallel()
		// The fixture's own doc comment narrates allocate_port(28090,
		// _key) in prose right above the real assignment. If the
		// scanner didn't strip KCL '#' comments, this would produce a
		// THIRD 28090 call site and change the finding count/shape.
		res, err := LintAllocatePortSpacing(root, 12, config.DefaultBlockSize)
		if err != nil {
			t.Fatalf("LintAllocatePortSpacing: %v", err)
		}
		got := findingsForRule(res.Findings, "forgeconv-allocate-port-spacing")
		count28090 := 0
		for _, f := range got {
			if strings.Contains(f.Message, "allocate_port(28090)") {
				count28090++
			}
		}
		// Exactly one pairing involves 28090 in this fixture (with
		// 29190); a comment double-count would produce a second,
		// self-paired or duplicated finding.
		if count28090 != 1 {
			t.Fatalf("expected exactly 1 finding naming 28090 (paired with 29190), got %d:\n%s",
				count28090, res.FormatText())
		}
	})
}

// TestLintAllocatePortSpacing_NoKCLDir verifies a project with no
// deploy/kcl directory (CLI/library shape) is a silent no-op, not an
// error — mirrors LintHandlerFileSize's missing-handlers/ contract.
func TestLintAllocatePortSpacing_NoKCLDir(t *testing.T) {
	t.Parallel()
	root := filepath.Join("testdata", "allocate_port_spacing", "no_kcl_dir")
	res, err := LintAllocatePortSpacing(root, config.DefaultMaxStacks, config.DefaultBlockSize)
	if err != nil {
		t.Fatalf("LintAllocatePortSpacing: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected 0 findings with no deploy/kcl dir, got %d:\n%s", len(res.Findings), res.FormatText())
	}
}

// TestLintAllocatePortSpacing_DefaultsWhenUnset verifies a caller that
// passes <=0 for maxStacks/blockSize gets config.Default{MaxStacks,
// BlockSize} rather than a degenerate always-fire or never-fire mode —
// the same "0 means unset" contract DevStackConfig.Effective* implements
// for the allocator itself, so a lint pass without a loaded project
// config still exercises real numbers.
func TestLintAllocatePortSpacing_DefaultsWhenUnset(t *testing.T) {
	t.Parallel()
	root := filepath.Join("testdata", "allocate_port_spacing", "full_set")

	withZero, err := LintAllocatePortSpacing(root, 0, 0)
	if err != nil {
		t.Fatalf("LintAllocatePortSpacing: %v", err)
	}
	withExplicitDefaults, err := LintAllocatePortSpacing(root, config.DefaultMaxStacks, config.DefaultBlockSize)
	if err != nil {
		t.Fatalf("LintAllocatePortSpacing: %v", err)
	}
	if len(withZero.Findings) != len(withExplicitDefaults.Findings) {
		t.Fatalf("maxStacks=0,blockSize=0 should behave identically to explicit defaults; "+
			"got %d vs %d findings", len(withZero.Findings), len(withExplicitDefaults.Findings))
	}
}

// TestLintAllocatePortSpacing_MessageNamesArithmetic pins the exact
// shape the finding message must carry: both bases with file:line, the
// shared remainder, and the exact colliding block plus resulting port —
// "these ports may collide" is explicitly insufficient per the rule's
// design brief, because it gives a reader nothing to act on.
func TestLintAllocatePortSpacing_MessageNamesArithmetic(t *testing.T) {
	t.Parallel()
	root := filepath.Join("testdata", "allocate_port_spacing", "full_set")
	res, err := LintAllocatePortSpacing(root, 12, config.DefaultBlockSize)
	if err != nil {
		t.Fatalf("LintAllocatePortSpacing: %v", err)
	}
	got := findingsForRule(res.Findings, "forgeconv-allocate-port-spacing")
	var hit *Finding
	for i := range got {
		if strings.Contains(got[i].Message, "allocate_port(28090)") && strings.Contains(got[i].Message, "allocate_port(29190)") {
			hit = &got[i]
		}
	}
	if hit == nil {
		t.Fatalf("expected the 28090/29190 finding; got:\n%s", res.FormatText())
	}
	for _, want := range []string{
		"28090", "29190", // both bases
		"main.k",        // file
		"congruent",     // states the relationship, not just "may collide"
		"mod 100",       // block size named
		"block 11",      // the exact block
		"max_stacks=12", // ties the reachability to the configured ceiling used in this test
	} {
		if !strings.Contains(hit.Message, want) {
			t.Errorf("message missing %q; got: %s", want, hit.Message)
		}
	}
	if hit.Remediation == "" {
		t.Error("expected a non-empty remediation pointing at moving one base off the congruence")
	}
}
