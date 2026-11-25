package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Each testdata/vuln_exempt/<case>.k is an env's main.k; <case>.k.golden is
// what `forge vuln exempt` writes into it. FORGE_UPDATE_GOLDEN=1 rewrites the
// goldens.
func TestExemptInKCLGolden(t *testing.T) {
	const id, reason, expires = "CVE-2026-1234", `unreachable: we never call the "parser"`, "2026-12-31"
	cases := []string{
		"vuln_gate_multiline_list", // an exemptions list: one more line
		"vuln_gate_inline_list",    // a one-line list stays one line
		"vuln_gate_no_exemptions",  // the literal gains exemptions
		"vuln_gate_one_line",       // {mode = "enforce"} gains , exemptions = [...]
		"delivery_policy_only",     // a policy with no vuln_gate gains one
		"bundle_only",              // a Bundle with no policy gains one
		"same_id_new_expiry",       // the one entry is replaced, not duplicated
		"strings_and_comments_lie", // a vuln_gate in a comment or string is not code
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			in, err := os.ReadFile(filepath.Join("testdata", "vuln_exempt", name+".k"))
			if err != nil {
				t.Fatal(err)
			}
			got, changed, err := exemptInKCL(string(in), id, reason, expires)
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("reported no change")
			}
			golden := filepath.Join("testdata", "vuln_exempt", name+".k.golden")
			if os.Getenv("FORGE_UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read %s (FORGE_UPDATE_GOLDEN=1 writes it): %v", golden, err)
			}
			if got != string(want) {
				t.Errorf("exempt %s:\n--- got\n%s\n--- want\n%s", name, got, want)
			}
			// Idempotent: the same exemption again changes nothing.
			again, changed, err := exemptInKCL(got, id, reason, expires)
			if err != nil || changed || again != got {
				t.Errorf("a second run changed the file (changed=%v, err=%v)", changed, err)
			}
		})
	}
}

func TestExemptInKCLRefusesWhatItCannotPlace(t *testing.T) {
	for name, src := range map[string]string{
		"two policies": `a = forge.DeliveryPolicy {vuln_gate = forge.VulnPolicy {mode = "audit"}}
b = forge.DeliveryPolicy {vuln_gate = forge.VulnPolicy {mode = "enforce"}}
`,
		"built elsewhere": `import ..base.env as envbase

output = envbase.render(envbase.bundle("prod"))
`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := exemptInKCL(src, "CVE-2026-1234", "why", "2026-12-31")
			var anchor *errVulnExemptNoAnchor
			if !errors.As(err, &anchor) {
				t.Fatalf("want a refusal with the lines to add, got %v", err)
			}
			if !strings.Contains(err.Error(), `forge.VulnExemption {id = "CVE-2026-1234", reason = "why", expires = "2026-12-31"}`) {
				t.Errorf("the refusal does not give the exemption to add:\n%s", err)
			}
		})
	}
}

func TestValidateVulnExemption(t *testing.T) {
	now := time.Date(2026, 11, 20, 23, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		id, reason, expires, want string
	}{
		{"CVE-2026-1", "why", "2026-11-20", ""}, // good through today
		{"GHSA-aaaa-bbbb-cccc", "why", "2027-01-01", ""},
		{"CVE-2026-1", "why", "2026-11-19", "in the past"},
		{"CVE-2026-1", "why", "2026/12/31", "not a YYYY-MM-DD"},
		{"CVE-2026-1", "why", "", "--expires"},
		{"CVE-2026-1", "  ", "2026-12-31", "--reason is required"},
		{"CVE-2026-1", "a\nb", "2026-12-31", "one line"},
		{"CVE-2026-1", "see ${x}", "2026-12-31", "interpolation"},
		{"not an id", "why", "2026-12-31", "not an advisory id"},
	} {
		err := validateVulnExemption(tc.id, tc.reason, tc.expires, now)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s %q %s: %v", tc.id, tc.reason, tc.expires, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s %q %s: got %v, want %q", tc.id, tc.reason, tc.expires, err, tc.want)
		}
	}
}

// exemptHarness is a project with deploy/kcl/prod/main.k and a stated render.
func exemptHarness(t *testing.T, mainK string, render func(path string) (release.ShapeVulnGate, error)) (vulnDeps, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "deploy", "kcl", "prod", "main.k")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(mainK), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := vulnExemptRender
	vulnExemptRender = func(context.Context, string, string) (release.ShapeVulnGate, error) { return render(path) }
	t.Cleanup(func() { vulnExemptRender = prev })
	return vulnDeps{projectDir: func() string { return dir }, now: func() time.Time { return vulnTestNow }}, path
}

// renderFromText reads exemptions back from the written file the way a KCL
// render would see the ones it parsed: enough to tell a reached edit from one
// that missed.
func renderFromText(path string) (release.ShapeVulnGate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return release.ShapeVulnGate{}, err
	}
	var g release.ShapeVulnGate
	if strings.Contains(string(b), `id = "CVE-2026-1234"`) {
		g.Exemptions = append(g.Exemptions, release.ShapeVulnExempt{ID: "CVE-2026-1234", Reason: "not reachable", Expires: "2026-12-31"})
	}
	return g, nil
}

const exemptMainK = `import forge

bundle = forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {mode = "enforce"}
    }
}
`

func TestRunVulnExemptWritesRendersAndIsIdempotent(t *testing.T) {
	deps, path := exemptHarness(t, exemptMainK, renderFromText)
	var out bytes.Buffer
	opts := vulnExemptOptions{reason: "not reachable", expires: "2026-12-31"}
	if err := runVulnExempt(context.Background(), &out, deps, "prod", "CVE-2026-1234", opts); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `vuln_gate = forge.VulnPolicy {mode = "enforce", exemptions = [forge.VulnExemption {id = "CVE-2026-1234", reason = "not reachable", expires = "2026-12-31"}]}`) {
		t.Fatalf("main.k:\n%s", b)
	}
	if !strings.Contains(out.String(), "exempted CVE-2026-1234 in env prod until 2026-12-31: deploy/kcl/prod/main.k") {
		t.Errorf("%s", out.String())
	}
	out.Reset()
	if err := runVulnExempt(context.Background(), &out, deps, "prod", "CVE-2026-1234", opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing changed") {
		t.Errorf("second run: %s", out.String())
	}
	if again, _ := os.ReadFile(path); !bytes.Equal(again, b) {
		t.Error("the second run rewrote main.k")
	}
}

// An edit the render does not see — the env reads its policy from somewhere
// else — is undone, never kept.
func TestRunVulnExemptRestoresAnEditTheRenderDoesNotSee(t *testing.T) {
	deps, path := exemptHarness(t, exemptMainK, func(string) (release.ShapeVulnGate, error) { return release.ShapeVulnGate{}, nil })
	err := runVulnExempt(context.Background(), &bytes.Buffer{}, deps, "prod", "CVE-2026-1234", vulnExemptOptions{reason: "not reachable", expires: "2026-12-31"})
	if err == nil || !strings.Contains(err.Error(), "does not reach env prod's rendered vuln_gate") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != exemptMainK {
		t.Errorf("main.k was not restored:\n%s", b)
	}
}

func TestRunVulnExemptRefusesAPastDate(t *testing.T) {
	deps, path := exemptHarness(t, exemptMainK, renderFromText)
	err := runVulnExempt(context.Background(), &bytes.Buffer{}, deps, "prod", "CVE-2026-1234", vulnExemptOptions{reason: "x", expires: "2026-01-01"})
	if err == nil || !strings.Contains(err.Error(), "in the past") {
		t.Fatalf("%v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != exemptMainK {
		t.Error("a refused exemption touched main.k")
	}
}

func TestExemptInEnvDocYAML(t *testing.T) {
	src := []byte(`apiVersion: forge.dev/env/v1
# the prod env
delivery_policy:
  vuln_gate:
    mode: enforce # opted in
`)
	got, changed, err := exemptInEnvDocYAML(src, "CVE-2026-1234", "not reachable", "2026-12-31")
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	for _, want := range []string{"# the prod env", "mode: enforce # opted in", "exemptions:", "- id: CVE-2026-1234", "reason: not reachable", `expires: "2026-12-31"`} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	again, changed, err := exemptInEnvDocYAML([]byte(got), "CVE-2026-1234", "not reachable", "2026-12-31")
	if err != nil || changed || again != got {
		t.Errorf("not idempotent: changed=%v err=%v", changed, err)
	}
	renewed, _, err := exemptInEnvDocYAML([]byte(got), "CVE-2026-1234", "still not reachable", "2027-03-31")
	if err != nil || strings.Count(renewed, "CVE-2026-1234") != 1 || !strings.Contains(renewed, "2027-03-31") {
		t.Errorf("renewal duplicated or lost the entry (%v):\n%s", err, renewed)
	}
	if _, _, err := exemptInEnvDocYAML([]byte("apiVersion: forge.dev/env/v1\n"), "CVE-2026-1234", "x", "2026-12-31"); err == nil {
		t.Error("a document with no vuln_gate was edited")
	}
}

// The review's cases (forge#2490): every one refuses, writes nothing, and
// prints the lines to add by hand. The AST, not the text, decides.
func TestExemptInKCLFailsClosed(t *testing.T) {
	const id = "CVE-2026-1234"
	cases := map[string]struct{ src, why string }{
		// An `id = "…"` inside another exemption's string, quotes escaped:
		// a regex over the text took it for a declaration.
		"id inside an escaped string": {`import forge

bundle = forge.Bundle {
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {
            exemptions = [
                forge.VulnExemption {id = "CVE-2026-9999", reason = "unlike id = \"CVE-2026-1234\", unreachable", expires = "2026-12-31"}
            ]
        }
    }
}
`, "appears in a string"},
		"computed id": {`import forge

_id = "CVE-2026-1234"
bundle = forge.Bundle {
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {exemptions = [forge.VulnExemption {id = _id, reason = "x", expires = "2026-12-31"}]}
    }
}
`, "computed id"},
		"computed field of the same exemption": {`import forge

_why = "unreachable"
bundle = forge.Bundle {
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {exemptions = [forge.VulnExemption {id = "CVE-2026-1234", reason = "${_why}", expires = "2026-12-31"}]}
    }
}
`, "not a plain literal"},
		"computed exemptions list": {`import forge

_ex = [forge.VulnExemption {id = "GO-2026-0001", reason = "x", expires = "2026-12-31"}]
bundle = forge.Bundle {
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {exemptions = _ex + []}
    }
}
`, "exemptions at line"},
		"a Bundle and an independent DeliveryPolicy": {`import forge

_policy = forge.DeliveryPolicy {approvals = forge.Approvals {mode = "enforce"}}
bundle = forge.Bundle {
    project = "demo"
}
`, "not its delivery_policy field"},
		"a Bundle with a computed delivery_policy": {`import forge
import ..base.env as envbase

bundle = forge.Bundle {
    project = "demo"
    delivery_policy = envbase.policy
}
`, "delivery_policy at line 6 is computed"},
		"a Bundle unpacking a dict": {`import forge

_extra = {project = "demo"}
bundle = forge.Bundle {
    **_extra
    env = "prod"
}
`, "unpacks a value"},
		"named only in a comment": {`import forge

# CVE-2026-1234 is accepted in lib/policy.k
bundle = forge.Bundle {
    project = "demo"
}
`, "only in a comment"},
		"two delivery_policy fields": {`import forge

bundle = forge.Bundle {
    delivery_policy = forge.DeliveryPolicy {}
}
other = {delivery_policy = forge.DeliveryPolicy {}}
`, "set 2 times"},
		"a syntax error": {"import forge\n\nbundle = forge.Bundle {\n    project = \n", "cannot parse"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, changed, err := exemptInKCL(tc.src, id, "why", "2026-12-31")
			var anchor *errVulnExemptNoAnchor
			if !errors.As(err, &anchor) || changed || got != "" {
				t.Fatalf("want a refusal, got changed=%v err=%v\n%s", changed, err, got)
			}
			if !strings.Contains(err.Error(), tc.why) || !strings.Contains(err.Error(), "Nothing was written") ||
				!strings.Contains(err.Error(), `VulnExemption {id = "CVE-2026-1234", reason = "why", expires = "2026-12-31"}`) {
				t.Errorf("the refusal must say why (%q) and give the lines to add:\n%s", tc.why, err)
			}
		})
	}
}

// An id written with an escape is the same id: the literal is replaced
// whole, and nothing outside it moves.
func TestExemptInKCLEscapedIDIsReplacedInPlace(t *testing.T) {
	src := `import forge

bundle = forge.Bundle {
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {
            exemptions = [
                forge.VulnExemption {id = "CVE\x2d2026-1234", reason = "old", expires = "2026-11-30"} # renewed below
            ]
        }
    }
}
`
	got, changed, err := exemptInKCL(src, "CVE-2026-1234", "new reason", "2027-03-31")
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	want := strings.Replace(src, `forge.VulnExemption {id = "CVE\x2d2026-1234", reason = "old", expires = "2026-11-30"}`,
		`forge.VulnExemption {id = "CVE-2026-1234", reason = "new reason", expires = "2027-03-31"}`, 1)
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// Nothing else changed: each golden edit differs from its input in one
// window, which removes nothing but (at most) the one exemption literal it
// replaced, and adds nothing but the new literal and the fields that hold it.
func TestExemptInKCLChangesOnlyTheLiteral(t *testing.T) {
	const id, reason, expires = "CVE-2026-1234", `unreachable: we never call the "parser"`, "2026-12-31"
	ins, _ := filepath.Glob(filepath.Join("testdata", "vuln_exempt", "*.k"))
	if len(ins) == 0 {
		t.Fatal("no golden inputs")
	}
	wrapper := regexp.MustCompile(`\b(exemptions|vuln_gate|delivery_policy)\b|\b[A-Za-z_][A-Za-z0-9_]*\.(VulnPolicy|DeliveryPolicy)\b|[\s,=\[\]{}]`)
	for _, in := range ins {
		orig, err := os.ReadFile(in)
		if err != nil {
			t.Fatal(err)
		}
		next, changed, err := exemptInKCL(string(orig), id, reason, expires)
		if err != nil || !changed {
			t.Fatalf("%s: %v %v", in, changed, err)
		}
		// The new literal and the one it replaced (same id) become one
		// placeholder; what is left may differ only by the fields that hold
		// the literal.
		newLit := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*\.VulnExemption \{id = "CVE-2026-1234", reason = "unreachable: we never call the \\"parser\\"", expires = "2026-12-31"\}`)
		oldLit := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*\.VulnExemption \{id = "CVE-2026-1234"[^}]*\}`)
		if n := len(newLit.FindAllStringIndex(next, -1)); n != 1 {
			t.Fatalf("%s: %d new literals", in, n)
		}
		a, b := newLit.ReplaceAllString(next, "\x00"), oldLit.ReplaceAllString(string(orig), "\x00")
		lo, hiB, hiA := kclChangedWindow(b, a)
		removed, added := b[lo:hiB], a[lo:hiA]
		if strings.Trim(wrapper.ReplaceAllString(removed, ""), "\x00") != "" {
			t.Errorf("%s: the edit removed more than the replaced literal: %q", in, removed)
		}
		if strings.Trim(wrapper.ReplaceAllString(added, ""), "\x00") != "" {
			t.Errorf("%s: the edit added more than the literal and the fields that hold it: %q", in, added)
		}
	}
}

// The post-edit check bites: a result that changed a byte outside the
// literal, or another exemption, is refused.
func TestVerifyKCLExemptEditRefusesCollateralChanges(t *testing.T) {
	orig := `import forge

bundle = forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {exemptions = [forge.VulnExemption {id = "GO-2026-0001", reason = "x", expires = "2026-12-31"}]}
    }
}
`
	a, err := parseKCLForEdit(orig)
	if err != nil {
		t.Fatal(err)
	}
	before := collectKCLFacts(a)
	lit := `forge.VulnExemption {id = "CVE-2026-1234", reason = "r", expires = "2026-12-31"}`
	at := strings.Index(orig, "]}")
	good := orig[:at] + ", " + lit + orig[at:]
	e := &kclExemptEdit{id: "CVE-2026-1234", reason: "r", expires: "2026-12-31", alias: "forge"}
	splice := func(next string) kclSplice {
		return kclSplice{next: next, lo: at, hi: at, adds: map[string]int{"VulnExemption": 1}}
	}
	if err := verifyKCLExemptEdit(e, orig, splice(good), before); err != nil {
		t.Fatalf("a clean insertion was refused: %v", err)
	}
	for name, bad := range map[string]string{
		"a byte outside the literal": strings.Replace(good, `project = "demo"`, `project = "other"`, 1),
		"another exemption changed":  strings.Replace(good, `reason = "x"`, `reason = "y"`, 1),
	} {
		if err := verifyKCLExemptEdit(e, orig, splice(bad), before); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
