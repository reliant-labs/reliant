package cigate

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/config"
)

// ci.license shapes the license-check argv. Without it the argv is the
// blocklist forge has always run, byte for byte.
func TestPlanLicenseCheck(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	gomod := "module example.com/shop\n\ngo 1.24\n\nrequire (\n\tgithub.com/google/uuid v1.6.0\n\tgithub.com/hashicorp/go-version v1.7.0\n)\n"
	for _, tc := range []struct {
		name    string
		policy  config.CILicenseConfig
		want    string
		expired string
	}{
		{
			name: "no policy: the blocklist, unchanged",
			want: "go-licenses check ./... --disallowed_types=forbidden,restricted --ignore example.com/shop",
		},
		{
			name:   "allowed: an allowlist instead of the blocklist",
			policy: config.CILicenseConfig{Allowed: []string{"Apache-2.0", " MIT ", "BSD-3-Clause"}},
			want:   "go-licenses check ./... --allowed_licenses=Apache-2.0,MIT,BSD-3-Clause --ignore example.com/shop",
		},
		{
			name: "exceptions: one --ignore each, after the project's own",
			policy: config.CILicenseConfig{
				Allowed: []string{"MIT"},
				Exceptions: []config.CILicenseException{
					{Module: "github.com/hashicorp/go-version", Reason: "MPL-2.0, unmodified"},
					{Module: "example.com/shop", Reason: "already ignored: not repeated"},
				},
			},
			want: "go-licenses check ./... --allowed_licenses=MIT --ignore example.com/shop --ignore github.com/hashicorp/go-version",
		},
		{
			name: "exceptions work in blocklist mode too",
			policy: config.CILicenseConfig{Exceptions: []config.CILicenseException{
				{Module: "github.com/hashicorp/go-version", Reason: "r"},
			}},
			want: "go-licenses check ./... --disallowed_types=forbidden,restricted --ignore example.com/shop --ignore github.com/hashicorp/go-version",
		},
		{
			name: "an entry dated today is still live; one dated yesterday is not",
			policy: config.CILicenseConfig{Allowed: []string{"MIT"}, Exceptions: []config.CILicenseException{
				{Module: "github.com/hashicorp/go-version", Reason: "r", Expires: "2026-11-25"},
				{Module: "github.com/google/uuid", Reason: "r", Expires: "2026-11-24"},
			}},
			want:    "go-licenses check ./... --allowed_licenses=MIT --ignore example.com/shop --ignore github.com/hashicorp/go-version",
			expired: "github.com/google/uuid (expired 2026-11-24)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "go.mod", gomod)
			lp := planLicenseCheck(root, tc.policy, now)
			if got := strings.Join(lp.argv, " "); got != tc.want {
				t.Errorf("argv:\n got %s\nwant %s", got, tc.want)
			}
			if got := strings.Join(lp.expired, ", "); got != tc.expired {
				t.Errorf("expired = %q, want %q", got, tc.expired)
			}
			if lp.overbroad != "" {
				t.Errorf("overbroad = %q, want none", lp.overbroad)
			}
		})
	}
}

// go-licenses matches --ignore as a raw prefix of package paths, so an
// exception for github.com/foo/bar also drops github.com/foo/barbaz and
// github.com/foo/bar/v2, distinct modules. The plan refuses such an
// exception, naming exactly those modules; its own packages, and a module
// that carries an exception of its own, are not reach.
func TestPlanLicenseCheck_RefusesAnExceptionsPrefixReach(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/shop\n\nrequire (\n\tgithub.com/foo/bar v1.0.0\n\tgithub.com/foo/bar/sub v1.0.0\n\tgithub.com/foo/barbaz v1.0.0\n\tgithub.com/foo/bar/v2 v2.0.0\n\tgithub.com/foo/bar-client v1.0.0\n)\n")
	ex := func(mods ...string) config.CILicenseConfig {
		var p config.CILicenseConfig
		for _, m := range mods {
			p.Exceptions = append(p.Exceptions, config.CILicenseException{Module: m, Reason: "r"})
		}
		return p
	}
	lp := planLicenseCheck(root, ex("github.com/foo/bar"), now)
	for _, want := range []string{"github.com/foo/bar would also exempt github.com/foo/barbaz", "github.com/foo/bar would also exempt github.com/foo/bar/v2", "github.com/foo/bar would also exempt github.com/foo/bar-client"} {
		if !strings.Contains(lp.overbroad, want) {
			t.Errorf("overbroad %q does not say %q", lp.overbroad, want)
		}
	}
	if strings.Contains(lp.overbroad, "exempt github.com/foo/bar/sub") {
		t.Errorf("overbroad %q names a module within the exception's path", lp.overbroad)
	}
	if lp := planLicenseCheck(root, ex("github.com/foo/bar", "github.com/foo/barbaz", "github.com/foo/bar/v2", "github.com/foo/bar-client"), now); lp.overbroad != "" {
		t.Errorf("every reached module has its own exception, yet overbroad = %q", lp.overbroad)
	}
	c := planner{PlanInput: PlanInput{Root: root, Cfg: &config.ProjectConfig{CI: config.CIConfig{License: ex("github.com/foo/bar")}}, Now: func() time.Time { return now }}}.licenseCheck()
	if !strings.Contains(c.Failed, "github.com/foo/barbaz") {
		t.Errorf("Failed = %q: an over-broad exception must fail the row", c.Failed)
	}
}

func TestExceptionCovers(t *testing.T) {
	for d, want := range map[string]bool{
		"github.com/foo/bar": true, "github.com/foo/bar/sub": true, "github.com/foo/bar/v1x": true,
		"github.com/foo/bar/v2": false, "github.com/foo/bar/v10/x": false, "github.com/foo/barbaz": false,
	} {
		if got := exceptionCovers("github.com/foo/bar", d); got != want {
			t.Errorf("exceptionCovers(bar, %s) = %v, want %v", d, got, want)
		}
	}
}

// An expired exception fails the row without running go-licenses, naming the
// entry; a live policy runs the row as usual.
func TestPlanner_LicenseCheckRow(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/shop\n")
	cfg := &config.ProjectConfig{}
	cfg.CI.License = config.CILicenseConfig{Allowed: []string{"MIT"}, Exceptions: []config.CILicenseException{
		{Module: "github.com/foo/bar", Reason: "r", Expires: "2026-01-31"},
	}}
	at := func(day string) Check {
		now, _ := time.Parse(time.DateOnly, day)
		now = now.Add(12 * time.Hour)
		return planner{PlanInput: PlanInput{Root: root, Cfg: cfg, Now: func() time.Time { return now }}}.licenseCheck()
	}
	if c := at("2026-01-31"); c.Failed != "" || !strings.Contains(strings.Join(c.Argv, " "), "--ignore github.com/foo/bar") {
		t.Errorf("live exception: Failed=%q argv=%v", c.Failed, c.Argv)
	}
	c := at("2026-02-01")
	if !strings.Contains(c.Failed, "github.com/foo/bar (expired 2026-01-31)") {
		t.Fatalf("expired exception: Failed = %q, want it named", c.Failed)
	}

	fx := &fakeExec{}
	rep := Run(context.Background(), "checks", []Check{c}, Options{Out: io.Discard, Exec: fx.run})
	if st := statuses(rep)["license-check"]; st != Fail || !rep.Failed() {
		t.Errorf("status = %s, failed = %v; want FAIL", st, rep.Failed())
	}
	if len(fx.ran) != 0 {
		t.Errorf("ran %v: a row the plan failed must not run", fx.ran)
	}
	if rep.Results[0].Note != c.Failed {
		t.Errorf("note = %q, want the expired entry", rep.Results[0].Note)
	}

	// CI_SKIP still takes it out, as for any row.
	rep = Run(context.Background(), "checks", []Check{c}, Options{Out: io.Discard, Exec: fx.run, Skip: []string{"license-check"}})
	if rep.Failed() {
		t.Error("CI_SKIP=license-check did not allow the failed row")
	}
}

// The license-check rollout row warns first (PlanInput.LicenseWarnFirst), but
// a project that declares a ci.license policy asked for the verdict: its row
// gates at once.
func TestPlanner_DeclaredLicensePolicyGatesAtOnce(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/shop\n")
	row := func(policy config.CILicenseConfig) Check {
		cfg := &config.ProjectConfig{}
		cfg.CI.License = policy
		return planner{PlanInput: PlanInput{Root: root, Cfg: cfg, LicenseWarnFirst: "does not gate yet"}}.licenseCheck()
	}
	if c := row(config.CILicenseConfig{}); c.WarnOnFail != "does not gate yet" {
		t.Errorf("no policy: WarnOnFail = %q, want the rollout's warning", c.WarnOnFail)
	}
	if c := row(config.CILicenseConfig{Allowed: []string{"MIT"}}); c.WarnOnFail != "" {
		t.Errorf("an allowlist: WarnOnFail = %q, want it to gate", c.WarnOnFail)
	}
	if c := row(config.CILicenseConfig{Exceptions: []config.CILicenseException{{Module: "github.com/foo/bar", Reason: "r"}}}); c.WarnOnFail != "" {
		t.Errorf("exceptions alone: WarnOnFail = %q, want it to gate", c.WarnOnFail)
	}
}
