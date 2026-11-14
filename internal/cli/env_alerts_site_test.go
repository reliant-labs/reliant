package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/release"
)

func withUptimeChecks(e *KCLEntities, names ...string) *KCLEntities {
	for _, n := range names {
		e.UptimeChecks = append(e.UptimeChecks, release.ShapeUptimeCheck{Name: n, Target: n, Method: "GET", IntervalSeconds: 60, TimeoutSeconds: 10, Source: release.UptimeSourceDefault})
	}
	return e
}

func siteAlertNames(got []release.ShapeAlert) string {
	var n []string
	for _, a := range got {
		n = append(n, a.Alert)
	}
	return strings.Join(n, ",")
}

const uptimeAlertNames = "CertificateExpiring,CertificateExpiringSoon,UptimeCheckDown,UptimeCheckFailing"

// A static site has no generated alert defaults, but its uptime checks get
// the uptime defaults, routed like every other default.
func TestResolveShapeAlerts_ASiteRecordsTheUptimeDefaults(t *testing.T) {
	site := t.TempDir()
	got, routes, err := resolveShapeAlerts(context.Background(), io.Discard, site, "prod", withUptimeChecks(obsEntities(nil), "web"))
	if err != nil {
		t.Fatal(err)
	}
	if names := siteAlertNames(got); names != uptimeAlertNames {
		t.Fatalf("a site with an uptime check records %q, want the four uptime rules", names)
	}
	for _, a := range got {
		if a.Source != release.AlertSourceDefault || !strings.Contains(a.Expr, "IN ('web')") || !strings.Contains(a.Expr, "= 'prod'") {
			t.Errorf("%s = %+v", a.Alert, a)
		}
	}
	if len(routes) != 2 || routes[0].Receiver != release.AlertReceiverInApp || routes[1].Receiver != release.AlertReceiverSlack {
		t.Errorf("routes = %+v, want in-app then Slack", routes)
	}

	// The same rules, byte for byte, as a project with generated defaults records.
	project := writeAlertDefaults(t, alertProject())
	full, _, err := resolveShapeAlerts(context.Background(), io.Discard, project, "prod", withUptimeChecks(obsEntities(nil), "web"))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]release.ShapeAlert{}
	for _, a := range full {
		by[a.Alert] = a
	}
	for _, a := range got {
		if b := by[a.Alert]; b.Expr != a.Expr || b.For != a.For || b.DefaultID != a.DefaultID || b.Labels["severity"] != a.Labels["severity"] {
			t.Errorf("%s differs from the project's:\nsite:    %+v\nproject: %+v", a.Alert, a, b)
		}
	}
}

func TestResolveShapeAlerts_ASiteOptsOutOfUptimeAlerts(t *testing.T) {
	site := t.TempDir()
	cases := map[string]*KCLEntities{
		"both defaults disabled": withUptimeChecks(obsEntities(&AlertsEntity{Defaults: map[string]AlertDefaultEntity{
			"uptime-check-failing": {Enabled: false}, "certificate-expiring": {Enabled: false},
		}}), "web"),
		"no uptime checks":  obsEntities(nil),
		"observability off": withUptimeChecks(&KCLEntities{Observability: &ObservabilityEntity{Enabled: false}}, "web"),
	}
	metricsOff := withUptimeChecks(obsEntities(nil), "web")
	metricsOff.Observability.Policy = &ObservabilityPolicyEntity{Enabled: true, Disabled: []string{"metrics"}}
	cases["metrics opted out"] = metricsOff
	for name, e := range cases {
		got, routes, err := resolveShapeAlerts(context.Background(), io.Discard, site, "prod", e)
		if err != nil || got != nil || routes != nil {
			t.Errorf("%s: got %q %v %v, want nothing", name, siteAlertNames(got), routes, err)
		}
	}
	// One disabled, the other kept.
	got, _, err := resolveShapeAlerts(context.Background(), io.Discard, site, "prod", withUptimeChecks(obsEntities(&AlertsEntity{Defaults: map[string]AlertDefaultEntity{
		"certificate-expiring": {Enabled: false},
	}}), "web"))
	if err != nil || siteAlertNames(got) != "UptimeCheckDown,UptimeCheckFailing" {
		t.Errorf("certificate-expiring disabled: got %q %v", siteAlertNames(got), err)
	}
	// A typo in the opt-out is refused, as for any project.
	if _, _, err := resolveShapeAlerts(context.Background(), io.Discard, site, "prod", withUptimeChecks(obsEntities(&AlertsEntity{Defaults: map[string]AlertDefaultEntity{
		"uptime-chek-failing": {Enabled: false},
	}}), "web")); err == nil {
		t.Error("a misspelled default id is accepted for a site")
	}
}

// renderSiteEnv renders a hosted static site env through the seam `forge env
// shape` uses; extra goes inside the forge.Bundle.
func renderSiteEnv(t *testing.T, extra string) *KCLEntities {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: renders KCL; runs in task test")
	}
	dir := t.TempDir()
	files := map[string]string{
		"kcl.mod": "[package]\nname = \"site\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k": `import forge

output = forge.render(forge.Bundle {
    project = "landing"
    control_plane = forge.ControlPlane {}
    frontends = [forge.Frontend {
        name = "web"
        image = "ghcr.io/acme/web"
        path = "."
        type = "vite"
        public_dir = "dist"
        runtime = forge.OnHosted {}
    }]
` + extra + `
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

// KCL -> render -> alerts, for a hosted static site with no generated alert
// defaults: its derived uptime check brings the uptime alerts, and each
// per-env opt-out removes them.
func TestHostedStaticSiteUptimeAlertsFollowTheRender(t *testing.T) {
	site := t.TempDir()
	resolve := func(extra string) []release.ShapeAlert {
		t.Helper()
		got, _, err := resolveShapeAlerts(context.Background(), io.Discard, site, "prod", renderSiteEnv(t, extra))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	got := resolve("    observability = forge.Observability {}\n")
	if siteAlertNames(got) != uptimeAlertNames {
		t.Fatalf("a hosted static site records %q, want the four uptime rules", siteAlertNames(got))
	}
	for _, a := range got {
		if !strings.Contains(a.Expr, "IN ('web')") {
			t.Errorf("%s does not read the site's derived check:\n%s", a.Alert, a.Expr)
		}
	}

	off := resolve(`    observability = forge.Observability {
        alerts = forge.Alerts {defaults = {
            "uptime-check-failing": forge.AlertDefault {enabled = False}
            "certificate-expiring": forge.AlertDefault {enabled = False}
        }}
    }
`)
	if off != nil {
		t.Errorf("both uptime defaults disabled in KCL: recorded %q", siteAlertNames(off))
	}
	if got := resolve("    observability = forge.Observability {}\n    uptime = forge.Uptime {defaults = False}\n"); got != nil {
		t.Errorf("no uptime checks (Uptime.defaults = False): recorded %q", siteAlertNames(got))
	}
}
