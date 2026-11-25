package cigate

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// goenvExec answers `go env GOROOT`, records the environment every other
// command ran with, and fails the commands named in fail.
type goenvExec struct {
	goroot string
	fail   map[string]bool
	ran    [][]string
	env    map[string][]string
}

func (g *goenvExec) run(_ context.Context, _ string, env []string, argv []string, out io.Writer) error {
	g.ran = append(g.ran, argv)
	if strings.Join(argv, " ") == "go env GOROOT" {
		if g.goroot == "" {
			return errors.New("go: not found")
		}
		_, _ = io.WriteString(out, g.goroot+"\n")
		return nil
	}
	if g.env == nil {
		g.env = map[string][]string{}
	}
	g.env[argv[0]] = env
	if g.fail[argv[0]] {
		_, _ = io.WriteString(out, "Restricted license type GPL-3.0 found for library example.com/gpl\n")
		return codedErr(1)
	}
	return nil
}

func goLicensesResolver(t *testing.T) *Resolver {
	return &Resolver{
		LookPath:  func(string) (string, error) { return "/bin/go-licenses", nil },
		Probe:     func(string, Tool) string { return goLicenses.Version },
		CacheRoot: t.TempDir(),
	}
}

func licenseRow() Check {
	c := check("license-check", "go-licenses", "check", "./...")
	c.Tools = []Tool{goLicenses}
	c.IsFinding = licenseFindingsOnly
	return c
}

// A pinned go-licenses is built with -trimpath, so it has no GOROOT of its
// own; without one in its environment it takes every package for the
// standard library and passes having checked nothing. The row runs it with
// GOROOT set to the project's `go env GOROOT`, and keeps a GOROOT the
// environment already sets.
func TestRun_GoLicensesRunsWithGOROOT(t *testing.T) {
	r := goLicensesResolver(t)
	c := licenseRow()

	fx := &goenvExec{goroot: "/usr/local/go"}
	rep := Run(context.Background(), "checks", []Check{c}, Options{Out: io.Discard, Exec: fx.run, Resolver: r, Env: []string{"PATH=/bin"}})
	if statuses(rep)["license-check"] != Pass {
		t.Fatalf("rows: %+v", rep.Results)
	}
	if got := envValue(fx.env["go-licenses"], "GOROOT"); got != "/usr/local/go" {
		t.Errorf("go-licenses ran with GOROOT=%q, want the project's /usr/local/go", got)
	}

	fx = &goenvExec{goroot: "/usr/local/go"}
	Run(context.Background(), "checks", []Check{c}, Options{Out: io.Discard, Exec: fx.run, Resolver: r, Env: []string{"PATH=/bin", "GOROOT=/opt/go"}})
	if got := envValue(fx.env["go-licenses"], "GOROOT"); got != "/opt/go" || len(fx.ran) != 1 {
		t.Errorf("GOROOT=%q ran=%v; a GOROOT the environment sets is kept, and go env is not asked", got, fx.ran)
	}

	fx = &goenvExec{}
	rep = Run(context.Background(), "checks", []Check{c}, Options{Out: io.Discard, Exec: fx.run, Resolver: r, Env: []string{"PATH=/bin"}})
	if statuses(rep)["license-check"] != Skip || !rep.Failed() || fx.env["go-licenses"] != nil {
		t.Errorf("no GOROOT to be had: rows=%+v; want SKIPPED (failing the gate) and go-licenses not run", rep.Results)
	}

	// A tool that does not need it gets no GOROOT and no `go env` call.
	plain := check("lint", "lint")
	fx = &goenvExec{}
	Run(context.Background(), "checks", []Check{plain}, Options{Out: io.Discard, Exec: fx.run, Env: []string{"PATH=/bin"}})
	if len(fx.ran) != 1 || envValue(fx.env["lint"], "GOROOT") != "" {
		t.Errorf("ran %v with GOROOT=%q; only a NeedsGOROOT tool gets one", fx.ran, envValue(fx.env["lint"], "GOROOT"))
	}
}

// While the license-check rollout row does not gate the project, a failing
// row warns: an allowed SKIPPED "would fail" carrying the finding and why it
// does not gate. From GatesFrom (no WarnOnFail) it FAILs.
func TestRun_LicenseCheckWarnsFirst(t *testing.T) {
	r := goLicensesResolver(t)
	c := licenseRow()
	c.WarnOnFail = "does not gate yet (new in v1.11.0; gates from v1.12.0)"
	fx := &goenvExec{goroot: "/usr/local/go", fail: map[string]bool{"go-licenses": true}}
	rep := Run(context.Background(), "checks", []Check{c}, Options{Out: io.Discard, Exec: fx.run, Resolver: r, Env: []string{"PATH=/bin"}})
	res := rep.Results[0]
	if res.Status != Skip || !res.Allowed || rep.Failed() {
		t.Fatalf("row %+v failed=%v; want an allowed SKIPPED row that does not fail the gate", res, rep.Failed())
	}
	if got := rep.verdict(); !strings.Contains(got, "1 skipped, not gating yet") || strings.Contains(got, "CI_SKIP") {
		t.Errorf("verdict %q: a warn-first row is not credited to CI_SKIP", got)
	}
	for _, want := range []string{"would fail", "GPL-3.0", "gates from v1.12.0"} {
		if !strings.Contains(res.Note, want) {
			t.Errorf("note %q does not say %q", res.Note, want)
		}
	}

	c.WarnOnFail = ""
	rep = Run(context.Background(), "checks", []Check{c}, Options{Out: io.Discard, Exec: fx.run, Resolver: r, Env: []string{"PATH=/bin"}})
	if statuses(rep)["license-check"] != Fail || !rep.Failed() {
		t.Errorf("rows %+v: a gating license-check must FAIL", rep.Results)
	}
}

func TestRun_LicenseScanErrorsStillFail(t *testing.T) {
	finding := "Restricted license type GPL-3.0 found for library example.com/gpl\n"
	for _, tc := range []struct {
		name, output string
		err          error
	}{
		{"package load", "Error: loading packages failed\n", codedErr(1)},
		{"parse", "Error: invalid input\n", codedErr(1)},
		{"empty", "", codedErr(1)},
		{"execution", finding, errors.New("cannot start process")},
		{"other exit", finding, codedErr(2)},
		{"mixed", finding + "Error: identifying license failed\n", codedErr(1)},
		{"error before long tail", "Error: scan incomplete\n" + strings.Repeat(finding, 200), codedErr(1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := licenseRow()
			c.WarnOnFail = "does not gate yet"
			rep := Run(context.Background(), "checks", []Check{c}, Options{
				Out: io.Discard, Resolver: goLicensesResolver(t), Env: []string{"GOROOT=/opt/go"},
				Exec: func(_ context.Context, _ string, _ []string, _ []string, out io.Writer) error {
					_, _ = io.WriteString(out, tc.output)
					return tc.err
				},
			})
			if res := rep.Results[0]; res.Status != Fail || res.Allowed || !rep.Failed() {
				t.Fatalf("scan error softened: %+v", res)
			}
		})
	}
}

// The planner hands the row PlanInput.LicenseWarnFirst, and the pinned tool
// asks for GOROOT.
func TestPlanner_LicenseCheckWarnFirst(t *testing.T) {
	if !goLicenses.NeedsGOROOT {
		t.Fatal("the pinned go-licenses must run with GOROOT set")
	}
	p := planner{PlanInput: PlanInput{Root: t.TempDir(), CI: serviceInput(t).CI, LicenseWarnFirst: "does not gate yet"}}
	if c := find(t, p.scanChecks(), "license-check"); c.WarnOnFail != "does not gate yet" {
		t.Errorf("WarnOnFail = %q", c.WarnOnFail)
	}
	p.LicenseWarnFirst = ""
	if c := find(t, p.scanChecks(), "license-check"); c.WarnOnFail != "" {
		t.Errorf("a gating project's row warns: %q", c.WarnOnFail)
	}
}
