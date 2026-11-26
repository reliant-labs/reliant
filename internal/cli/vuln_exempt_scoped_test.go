package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

const scopedExemptMainK = `import forge

bundle = forge.Bundle {
    project = "demo"
    delivery_policy = forge.DeliveryPolicy {
        vuln_gate = forge.VulnPolicy {
            mode = "enforce"
            exemptions = [
                forge.VulnExemption {id = "GO-2026-6859", reason = "grype never parses untrusted archives", expires = "2026-12-31", artifacts = ["api"], locations = ["/usr/local/bin/grype"]}
            ]
        }
    }
}
`

// An exemption's identity is its id AND its scope: the same scope is the one
// entry (renewed in place, idempotent), another scope is a second entry, and
// the edit never touches the other declaration of the same advisory.
func TestExemptInKCLScoped(t *testing.T) {
	grype := release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Reason: "grype never parses untrusted archives", Expires: "2026-12-31", Artifacts: []string{"api"}, Locations: []string{"/usr/local/bin/grype"}}

	got, changed, err := exemptInKCLOf(scopedExemptMainK, grype)
	if err != nil || changed || got != scopedExemptMainK {
		t.Fatalf("the same scoped exemption again: changed=%v err=%v", changed, err)
	}

	renewed := grype
	renewed.Reason, renewed.Expires = "still unreachable", "2027-03-31"
	got, changed, err = exemptInKCLOf(scopedExemptMainK, renewed)
	if err != nil || !changed {
		t.Fatalf("renewal: changed=%v err=%v", changed, err)
	}
	if strings.Count(got, "GO-2026-6859") != 1 || !strings.Contains(got, `reason = "still unreachable", expires = "2027-03-31", artifacts = ["api"], locations = ["/usr/local/bin/grype"]}`) {
		t.Errorf("renewal did not replace the one entry:\n%s", got)
	}

	worker := release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Reason: "the worker never serves http", Expires: "2026-12-31", Packages: []string{"stdlib"}, Artifacts: []string{"worker"}}
	got, changed, err = exemptInKCLOf(scopedExemptMainK, worker)
	if err != nil || !changed {
		t.Fatalf("another scope: changed=%v err=%v", changed, err)
	}
	if strings.Count(got, "GO-2026-6859") != 2 || !strings.Contains(got, scopedExemptMainK[strings.Index(scopedExemptMainK, "forge.VulnExemption"):strings.Index(scopedExemptMainK, "\n            ]")]) {
		t.Errorf("another scope must add an entry and keep the first:\n%s", got)
	}
	if !strings.Contains(got, `forge.VulnExemption {id = "GO-2026-6859", reason = "the worker never serves http", expires = "2026-12-31", packages = ["stdlib"], artifacts = ["worker"]}`) {
		t.Errorf("the second entry is not as written:\n%s", got)
	}
	// And now each of the two is found again by its own scope.
	for _, ex := range []release.ShapeVulnScopedExempt{grype, worker} {
		if again, changed, err := exemptInKCLOf(got, ex); err != nil || changed || again != got {
			t.Errorf("%s %v again: changed=%v err=%v", ex.ID, ex.Artifacts, changed, err)
		}
	}

	unscoped := release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Reason: "nothing calls it", Expires: "2026-12-31"}
	got, changed, err = exemptInKCLOf(scopedExemptMainK, unscoped)
	if err != nil || !changed || strings.Count(got, "GO-2026-6859") != 2 {
		t.Fatalf("an unscoped exemption beside a scoped one: changed=%v err=%v\n%s", changed, err, got)
	}
}

func TestExemptInKCLScopedRefusesAComputedScope(t *testing.T) {
	src := strings.Replace(scopedExemptMainK, `artifacts = ["api"]`, `artifacts = _artifacts`, 1)
	_, _, err := exemptInKCLOf(src, release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Reason: "r", Expires: "2026-12-31", Artifacts: []string{"worker"}})
	var anchor *errVulnExemptNoAnchor
	if !errors.As(err, &anchor) || !strings.Contains(err.Error(), "not a plain literal") {
		t.Fatalf("want a refusal naming the computed exemption, got %v", err)
	}
	if !strings.Contains(err.Error(), `artifacts = ["worker"]`) {
		t.Errorf("the refusal does not give the scoped exemption to add:\n%s", err)
	}
}

func TestValidateVulnExemptScope(t *testing.T) {
	for _, tc := range []struct {
		opts vulnExemptOptions
		want string
	}{
		{vulnExemptOptions{locations: []string{"/usr/local/bin/grype"}, artifacts: []string{"api"}, packages: []string{"pkg:golang/stdlib"}}, ""},
		{vulnExemptOptions{locations: []string{"usr/local/bin/grype"}}, "absolute path"},
		{vulnExemptOptions{artifacts: []string{""}}, "one non-empty line"},
		{vulnExemptOptions{packages: []string{" stdlib"}}, "padded"},
		{vulnExemptOptions{packages: []string{"${x}"}}, "interpolation"},
		{vulnExemptOptions{artifacts: []string{"api", "api"}}, "twice"},
	} {
		err := validateVulnExemptScope(tc.opts)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%+v: %v", tc.opts, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%+v: got %v, want %q", tc.opts, err, tc.want)
		}
	}
}

func TestRunVulnExemptScoped(t *testing.T) {
	render := func(path string) (release.ShapeVulnGate, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return release.ShapeVulnGate{}, err
		}
		var g release.ShapeVulnGate
		if strings.Contains(string(b), `locations = ["/usr/local/bin/grype"]`) {
			g.ScopedExemptions = []release.ShapeVulnScopedExempt{{ID: "CVE-2026-1234", Reason: "not reachable", Expires: "2026-12-31", Locations: []string{"/usr/local/bin/grype"}}}
		}
		return g, nil
	}
	deps, path := exemptHarness(t, exemptMainK, render)
	var out bytes.Buffer
	opts := vulnExemptOptions{reason: "not reachable", expires: "2026-12-31", locations: []string{"/usr/local/bin/grype"}}
	if err := runVulnExempt(context.Background(), &out, deps, "prod", "CVE-2026-1234", opts); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `exemptions = [forge.VulnExemption {id = "CVE-2026-1234", reason = "not reachable", expires = "2026-12-31", locations = ["/usr/local/bin/grype"]}]`) {
		t.Fatalf("main.k:\n%s", b)
	}
	if !strings.Contains(out.String(), "exempted CVE-2026-1234 (location /usr/local/bin/grype) in env prod") {
		t.Errorf("%s", out.String())
	}
	// The render carrying only an UNSCOPED exemption of the id is not this
	// edit reaching it: gateExempts compares the scope too.
	if gateExempts(release.ShapeVulnGate{Exemptions: []release.ShapeVulnExempt{{ID: "CVE-2026-1234", Reason: "not reachable", Expires: "2026-12-31"}}}, opts.exemption("CVE-2026-1234")) {
		t.Error("an unscoped exemption in the render was taken for the scoped one written")
	}
}

func TestExemptInEnvDocYAMLScoped(t *testing.T) {
	src := []byte(`apiVersion: forge.dev/env/v1
delivery_policy:
  vuln_gate:
    mode: enforce
    exemptions:
      - id: CVE-2026-1234
        reason: nothing calls it
        expires: "2026-12-31"
`)
	scoped := release.ShapeVulnScopedExempt{ID: "CVE-2026-1234", Reason: "grype only", Expires: "2026-12-31", Locations: []string{"/usr/local/bin/grype"}}
	got, changed, err := exemptInEnvDocYAMLOf(src, scoped)
	if err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	for _, want := range []string{"reason: nothing calls it", "scoped_exemptions:", "reason: grype only", "locations: [/usr/local/bin/grype]"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	again, changed, err := exemptInEnvDocYAMLOf([]byte(got), scoped)
	if err != nil || changed || again != got {
		t.Errorf("not idempotent: changed=%v err=%v\n%s", changed, err, again)
	}
	scoped.Expires = "2027-01-31"
	renewed, _, err := exemptInEnvDocYAMLOf([]byte(got), scoped)
	if err != nil || strings.Count(renewed, "CVE-2026-1234") != 2 || !strings.Contains(renewed, "2027-01-31") || !strings.Contains(renewed, "nothing calls it") {
		t.Errorf("renewal of the scoped entry (%v):\n%s", err, renewed)
	}
}
