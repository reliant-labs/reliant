package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
)

// renderUptime renders a hosted env with the given `uptime = ...` expression
// ("" for none) through the seam `forge env shape` uses.
func renderUptime(t *testing.T, decl string) *KCLEntities {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: renders KCL; runs in task test")
	}
	dir := t.TempDir()
	if decl != "" {
		decl = "uptime = " + decl
	}
	files := map[string]string{
		"kcl.mod": "[package]\nname = \"uptime\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k": `import forge
import forge.workloads as fw

output = forge.render(forge.Bundle {
    project = "demo"
    control_plane = forge.ControlPlane {}
    observability = forge.Observability {}
    workloads = [fw.Workload {
        name = "api"
        image = "ghcr.io/acme/api"
        ports = [fw.Port {name = "http", port = 8080, expose = True}]
        runtime = forge.OnHosted {}
    }]
    frontends = [forge.Frontend {
        name = "site"
        image = "ghcr.io/acme/site"
        path = "site"
        type = "vite"
        public_dir = "dist"
        runtime = forge.OnHosted {}
    }]
    ` + decl + `
})
`,
	}
	for f, c := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	e, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return e
}

func uptimeNames(checks []release.ShapeUptimeCheck) string {
	var n []string
	for _, c := range checks {
		n = append(n, c.Name)
	}
	return strings.Join(n, ",")
}

// KCL -> render contract -> shape field -> Shape.Validate, for the checks forge
// derives with no declaration and for the ones the author adds.
func TestUptimeChecksReachTheShape(t *testing.T) {
	e := renderUptime(t, "")
	got := shapeUptimeChecksOf(e)
	if uptimeNames(got) != "api,site" {
		t.Fatalf("a hosted env with an exposed workload and a frontend derived %q, want api,site", uptimeNames(got))
	}
	for _, c := range got {
		if c.Source != release.UptimeSourceDefault || c.Target != c.Name || c.Method != "GET" || c.IntervalSeconds != 60 || c.TimeoutSeconds != 10 {
			t.Errorf("derived check = %+v", c)
		}
	}
	if err := (release.Shape{Kind: release.EnvPersistent, UptimeChecks: got}).Canonical().Validate(); err != nil {
		t.Errorf("checks KCL rendered must satisfy the shape's validation: %v", err)
	}

	e = renderUptime(t, `forge.Uptime {
        skip = ["site"]
        checks = [forge.UptimeCheck {name = "shop", url = "https://shop.example.com/healthz", expect_body = "ok"}]
    }`)
	got = shapeUptimeChecksOf(e)
	if uptimeNames(got) != "api,shop" {
		t.Fatalf("skip + declared = %q, want api,shop", uptimeNames(got))
	}
	if shop := got[1]; shop.Source != release.UptimeSourceUser || shop.URL != "https://shop.example.com/healthz" || shop.ExpectBody != "ok" {
		t.Errorf("declared check = %+v", shop)
	}

	if shapeUptimeChecksOf(renderUptime(t, "forge.Uptime {defaults = False}")) != nil {
		t.Error("defaults = False still recorded checks")
	}
}

// The checks an env has decide whether its uptime alerts exist, and which
// checks they read: a check the env opted out of can never alert.
func TestUptimeChecksArmTheAlertDefaults(t *testing.T) {
	dir := writeAlertDefaults(t, alertProject())
	rules := func(e *KCLEntities) map[string]release.ShapeAlert {
		t.Helper()
		got, _, err := resolveShapeAlerts(dir, "prod", e)
		if err != nil {
			t.Fatal(err)
		}
		by := map[string]release.ShapeAlert{}
		for _, a := range got {
			by[a.Alert] = a
		}
		return by
	}

	e := renderUptime(t, "")
	by := rules(e)
	for _, name := range []string{"UptimeCheckFailing", "UptimeCheckDown", "CertificateExpiring", "CertificateExpiringSoon"} {
		a, ok := by[name]
		if !ok {
			t.Errorf("%s missing for an env with checks", name)
			continue
		}
		if a.Source != release.AlertSourceDefault || !strings.Contains(a.Expr, "IN ('api', 'site')") {
			t.Errorf("%s = source %q, expr scoped wrong:\n%s", name, a.Source, a.Expr)
		}
	}

	if by := rules(renderUptime(t, "forge.Uptime {defaults = False}")); by["UptimeCheckFailing"].Alert != "" || by["CertificateExpiring"].Alert != "" {
		t.Error("an env with no uptime checks still carries uptime alerts")
	}
	if by := rules(renderUptime(t, `forge.Uptime {skip = ["site"]}`)); !strings.Contains(by["UptimeCheckFailing"].Expr, "IN ('api')") || strings.Contains(by["UptimeCheckFailing"].Expr, "'site'") {
		t.Errorf("a skipped check is still read by the alert:\n%s", by["UptimeCheckFailing"].Expr)
	}
}
