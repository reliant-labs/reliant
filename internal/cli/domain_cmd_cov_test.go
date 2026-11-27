package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/exitcode"
)

// covADomainCmd runs `forge domain <args>` against a fake control plane,
// bypassing the KCL render that normally resolves the endpoint.
func covADomainCmd(t *testing.T, f *fakeDomainCP, args ...string) (string, error) {
	t.Helper()
	c := domainTestClient(t, f)
	prev := domainClient
	var gotEnv, gotToken string
	domainClient = func(_ context.Context, env, token string) (cloudCaller, error) {
		gotEnv, gotToken = env, token
		return c, nil
	}
	t.Cleanup(func() { domainClient = prev })
	var out bytes.Buffer
	cmd := newDomainCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	if err == nil && (gotEnv != "prod" || gotToken != "tok-flag") {
		t.Errorf("--env/--token reached the client as %q/%q", gotEnv, gotToken)
	}
	return out.String(), err
}

func covAHoundersProject(t *testing.T) {
	t.Helper()
	prev := hostedProjectName
	hostedProjectName = func() string { return "hounders" }
	t.Cleanup(func() { hostedProjectName = prev })
}

const (
	covALiveDomain = `{"domain":{"id":"dom_01","hostname":"hounders.club","state":"DEPLOY_CUSTOM_DOMAIN_STATE_LIVE","source":"DOMAIN_SOURCE_PLATFORM",
	  "liveSince":"2026-11-01T00:00:00Z","verifiedAt":"2026-10-30T00:00:00Z","servingPath":"DEPLOY_SERVING_PATH_EDGE",
	  "binding":{"id":"b1","domainId":"dom_01","environmentId":"env-7","target":"web","hstsIncludeSubdomains":true}}}`
	covADomainEnvList = `{"environments":[{"id":"env-7","name":"prod","project":"hounders"}]}`
)

func TestDomainCommandsThroughCobra(t *testing.T) {
	covAHoundersProject(t)
	common := []string{"--env", "prod", "--token", "tok-flag"}

	t.Run("add", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"CreateDomain": domainPendingJSON})
		out, err := covADomainCmd(t, f, append([]string{"add", "hounders.club"}, common...)...)
		if err != nil {
			t.Fatal(err)
		}
		if f.bodies["CreateDomain"]["hostname"] != "hounders.club" {
			t.Errorf("sent %v", f.bodies["CreateDomain"])
		}
		for _, want := range []string{"Added hounders.club (pending_dns)", "34.63.203.181", "domain verify hounders.club --env prod", "domain bind hounders.club --env prod --target"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("add of an already bound domain does not suggest binding", func(t *testing.T) {
		var out bytes.Buffer
		f := newFakeDomainCP(t, map[string]string{"CreateDomain": covALiveDomain})
		if err := runDomainAdd(context.Background(), domainTestClient(t, f), "hounders.club", false, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "serves nothing until you bind it") || !strings.Contains(out.String(), "--env <env>") {
			t.Errorf("output:\n%s", out.String())
		}
	})
	t.Run("add failure names the hostname", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{})
		err := runDomainAdd(context.Background(), covAFailingCaller{errors.New("boom")}, "x.test", false, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), `add domain "x.test": boom`) {
			t.Fatalf("err = %v", err)
		}
		_ = f
	})
	t.Run("ls", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"ListDomains": `{"domains":[` + strings.TrimSuffix(strings.TrimPrefix(covALiveDomain, `{"domain":`), `}`) + `,` +
			strings.TrimSuffix(strings.TrimPrefix(domainPendingJSON, `{"domain":`), `}`) + `]}`})
		out, err := covADomainCmd(t, f, append([]string{"ls"}, common...)...)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"HOSTNAME", "live", "web in env-7", "edge", "includeSubDomains", "pending_dns", "(unbound)"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("list --json and failures", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"ListDomains": `{"domains":[]}`})
		out, err := covADomainCmd(t, f, append([]string{"list", "--json"}, common...)...)
		if err != nil || !strings.Contains(out, `"domains"`) {
			t.Fatalf("out=%q err=%v", out, err)
		}
		if err := runDomainList(context.Background(), covAFailingCaller{errors.New("down")}, false, &bytes.Buffer{}); err == nil || err.Error() != "down" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("show names the env it was run with", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"GetDomain": covALiveDomain, "ListEnvironments": covADomainEnvList})
		out, err := covADomainCmd(t, f, append([]string{"show", "hounders.club"}, common...)...)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"state:   live", "source:  platform", "serving: web in env prod", "hsts:    includeSubDomains", "live:    since 2026-11-01", "proven:  2026-10-30"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})
	t.Run("show falls back to the env id when the lookup fails", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"GetDomain": covALiveDomain, "ListEnvironments": `{"environments":[]}`})
		out, err := covADomainCmd(t, f, append([]string{"show", "hounders.club"}, common...)...)
		if err != nil || !strings.Contains(out, "web in env-7") {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})
	t.Run("show of an unbound domain does no environment lookup", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"GetDomain": domainPendingJSON})
		if _, err := covADomainCmd(t, f, append([]string{"show", "hounders.club", "--json"}, common...)...); err != nil {
			t.Fatal(err)
		}
		for _, p := range f.order {
			if p == "ListEnvironments" {
				t.Errorf("calls = %v", f.order)
			}
		}
	})
	t.Run("an unknown hostname", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"GetDomain": `{"domain":{}}`})
		_, err := covADomainCmd(t, f, append([]string{"show", "nope.test"}, common...)...)
		if err == nil || !strings.Contains(err.Error(), `the control plane returned no domain for "nope.test"`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a lookup failure is wrapped with the hostname", func(t *testing.T) {
		_, err := lookupDomain(context.Background(), covAFailingCaller{errors.New("denied")}, "x.test")
		if err == nil || !strings.Contains(err.Error(), `look up domain "x.test": denied`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("rm", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"GetDomain": covALiveDomain, "DeleteDomain": `{}`})
		out, err := covADomainCmd(t, f, append([]string{"rm", "hounders.club"}, common...)...)
		if err != nil || !strings.Contains(out, "Removed hounders.club") || f.bodies["DeleteDomain"]["domainId"] != "dom_01" {
			t.Fatalf("out=%q err=%v body=%v", out, err, f.bodies["DeleteDomain"])
		}
	})
	t.Run("rm and unbind and verify report their failing call", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"GetDomain": covALiveDomain})
		c := &covAFailOn{inner: domainTestClient(t, f), proc: procDeleteDomain}
		if err := runDomainRemove(context.Background(), c, "hounders.club", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `remove domain "hounders.club"`) {
			t.Errorf("rm: %v", err)
		}
		c.proc = procDeleteDomainBinding
		if err := runDomainUnbind(context.Background(), c, "hounders.club", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `unbind domain "hounders.club"`) {
			t.Errorf("unbind: %v", err)
		}
		c.proc = procVerifyDomain
		if err := runDomainVerify(context.Background(), c, "hounders.club", false, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `verify domain "hounders.club"`) {
			t.Errorf("verify: %v", err)
		}
		c.proc = procCreateDomainBinding
		f.replies["ListEnvironments"] = covADomainEnvList
		if err := runDomainBind(context.Background(), c, "prod", "hounders.club", domainBindOptions{target: "web"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), `bind domain "hounders.club"`) {
			t.Errorf("bind: %v", err)
		}
	})
	t.Run("unbind", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"GetDomain": covALiveDomain, "DeleteDomainBinding": `{}`})
		out, err := covADomainCmd(t, f, append([]string{"unbind", "hounders.club"}, common...)...)
		if err != nil || !strings.Contains(out, "Unbound hounders.club. The domain and its verification are kept") {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})
	t.Run("bind with a target and HSTS", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"GetDomain": covALiveDomain, "ListEnvironments": covADomainEnvList, "CreateDomainBinding": `{"binding":{}}`})
		out, err := covADomainCmd(t, f, append([]string{"bind", "hounders.club", "--target", "web", "--hsts-include-subdomains"}, common...)...)
		if err != nil {
			t.Fatal(err)
		}
		if f.bodies["CreateDomainBinding"]["hstsIncludeSubdomains"] != true {
			t.Errorf("sent %v", f.bodies["CreateDomainBinding"])
		}
		if strings.Contains(out, "now OFF") || strings.Contains(out, "will start serving") {
			t.Errorf("a live domain that keeps HSTS prints neither warning:\n%s", out)
		}
	})
	t.Run("bind in an env the control plane does not know", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"GetDomain": covALiveDomain, "ListEnvironments": `{"environments":[]}`})
		err := runDomainBind(context.Background(), domainTestClient(t, f), "typo", "hounders.club", domainBindOptions{target: "web"}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "typo") {
			t.Fatalf("err = %v", err)
		}
		if _, ok := f.bodies["CreateDomainBinding"]; ok {
			t.Error("a typo'd --env must never bind")
		}
	})
	t.Run("an environment lookup failure is reported and tolerated by show", func(t *testing.T) {
		_, err := domainEnvironmentID(context.Background(), covAFailingCaller{errors.New("x")}, "prod")
		if err == nil || !strings.Contains(err.Error(), `resolve hosted environment "prod": x`) {
			t.Fatalf("err = %v", err)
		}
		if names := domainEnvNames(context.Background(), covAFailingCaller{errors.New("x")}, "prod"); names != nil {
			t.Errorf("names = %v", names)
		}
		if names := domainEnvNames(context.Background(), nil, ""); names != nil {
			t.Errorf("names = %v", names)
		}
	})
	t.Run("bind with neither or both is refused before any call", func(t *testing.T) {
		for _, args := range [][]string{{"bind", "hounders.club"}, {"bind", "hounders.club", "--target", "web", "--redirect-to", "x.test"}} {
			f := newFakeDomainCP(t, map[string]string{})
			c := domainTestClient(t, f)
			prev := domainClient
			domainClient = func(context.Context, string, string) (cloudCaller, error) { return c, nil }
			cmd := newDomainCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(append(args, "--env", "prod"))
			err := cmd.ExecuteContext(context.Background())
			domainClient = prev
			if err == nil || !strings.Contains(err.Error(), "give exactly one of --target or --redirect-to") || len(f.order) != 0 {
				t.Errorf("%v: err=%v calls=%v", args, err, f.order)
			}
		}
	})
}

func TestDomainVerifyRequireVerifiedExitCodes(t *testing.T) {
	covAHoundersProject(t)
	state := func(s, errMsg string) string {
		return `{"domain":{"id":"dom_01","hostname":"hounders.club","state":"` + s + `","lastError":"` + errMsg + `"}}`
	}
	tests := []struct {
		state, lastErr string
		exit           int
		want           string
	}{
		{"DEPLOY_CUSTOM_DOMAIN_STATE_LIVE", "", 0, ""},
		{"DEPLOY_CUSTOM_DOMAIN_STATE_ISSUING", "", 0, ""},
		{"DEPLOY_CUSTOM_DOMAIN_STATE_PENDING_DNS", "", exitTimedOut, "hounders.club is pending_dns: its DNS is not verified yet; check again later"},
		{"DEPLOY_CUSTOM_DOMAIN_STATE_VERIFYING", "", exitTimedOut, "is verifying"},
		{"DEPLOY_CUSTOM_DOMAIN_STATE_FAILED", "CAA forbids issuance", exitWrong, "hounders.club is failed: CAA forbids issuance"},
		{"DEPLOY_CUSTOM_DOMAIN_STATE_CONFLICT", "", exitWrong, "see `forge domain show`"},
		{"DEPLOY_CUSTOM_DOMAIN_STATE_SOMETHING_NEW", "", exitUndetermined, "a state forge does not know"},
	}
	for _, tc := range tests {
		t.Run(tc.state, func(t *testing.T) {
			f := newFakeDomainCP(t, map[string]string{"GetDomain": state(tc.state, tc.lastErr), "VerifyDomain": state(tc.state, tc.lastErr)})
			_, err := covADomainCmd(t, f, "verify", "hounders.club", "--require-verified", "--env", "prod", "--token", "tok-flag")
			if got := exitcode.Of(err); got != tc.exit {
				t.Fatalf("exit = %d (%v), want %d", got, err, tc.exit)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
	t.Run("without --require-verified a pending domain exits 0", func(t *testing.T) {
		f := newFakeDomainCP(t, map[string]string{"GetDomain": domainPendingJSON, "VerifyDomain": domainPendingJSON})
		out, err := covADomainCmd(t, f, "verify", "hounders.club", "--json", "--env", "prod", "--token", "tok-flag")
		if err != nil || !strings.Contains(out, `"hostname": "hounders.club"`) {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})
}

func TestDomainSmallRenderers(t *testing.T) {
	if got := servingPathCell(""); got != "-" {
		t.Errorf("servingPathCell(\"\") = %q", got)
	}
	for in, want := range map[string]string{"DOMAIN_SOURCE_EXTERNAL": "external", " domain_source_platform ": "platform", "": "unknown", "DOMAIN_SOURCE_NEW": "unknown"} {
		if got := domainSourceName(in); got != want {
			t.Errorf("domainSourceName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := bindingSummary(&domainWireBinding{RedirectTo: "apex.test"}); got != "→ apex.test (redirect)" {
		t.Errorf("redirect: %q", got)
	}
	if got := bindingSummary(&domainWireBinding{Target: "web", EnvironmentID: "e1"}, nil, map[string]string{"e1": "prod"}); got != "web in env prod" {
		t.Errorf("a later map that knows the env names it: %q", got)
	}
	if got := hstsCell(&domainWireBinding{}); got != "host only" {
		t.Errorf("hsts = %q", got)
	}
	var out bytes.Buffer
	writeDomainDetail(&out, domainWire{Hostname: "h.test", LastError: "dns lookup failed"})
	if !strings.Contains(out.String(), "error:   dns lookup failed") || strings.Contains(out.String(), "hsts:") || strings.Contains(out.String(), "path:") {
		t.Errorf("detail:\n%s", out.String())
	}
}

type covAFailingCaller struct{ err error }

func (c covAFailingCaller) Call(context.Context, string, any, any) error { return c.err }

// covAFailOn passes every call through except one procedure, which fails.
type covAFailOn struct {
	inner cloudCaller
	proc  string
}

func (c *covAFailOn) Call(ctx context.Context, proc string, req, out any) error {
	if proc == c.proc {
		return errors.New("simulated " + proc)
	}
	return c.inner.Call(ctx, proc, req, out)
}
