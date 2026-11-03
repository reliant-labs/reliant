// Copyright (c) 2025 Reliant Labs

package amconfig

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/alertmanager/config"
	"github.com/prometheus/alertmanager/dispatch"
	"github.com/prometheus/common/model"
)

func baseSettings() Settings {
	return Settings{
		InAppURL:       "http://admin-server.cp.svc:8090/internal/alerting/webhook",
		InAppTokenFile: SecretDir + "/inapp-token",
		SMTP:           SMTP{Host: "smtp.resend.com:587", From: "alerts@reliantlabs.io", Username: "resend", PasswordFile: SecretDir + "/smtp-password"},
	}
}

// fixture: two orgs that BOTH call a receiver "oncall", with env routes that
// match the same labels, so a collision or a widening route would show.
func fixture() Input {
	return Input{
		Settings: baseSettings(),
		Receivers: []Receiver{
			{OrgID: "org-a", Name: "oncall", Type: TypeSlack, SecretRef: "org-a.oncall"},
			{OrgID: "org-a", Name: "inapp", Type: TypeInApp},
			{OrgID: "org-a", Name: "mail", Type: TypeEmail, Settings: map[string]string{"to": "a@a.test"}},
			{OrgID: "org-b", Name: "oncall", Type: TypePagerDuty, SecretRef: "org-b.oncall"},
			{OrgID: "org-b", Name: "hook", Type: TypeWebhook, SecretRef: "org-b.hook", Settings: map[string]string{"url": "https://b.test/hook"}},
			{OrgID: "org-c-no-recv", Name: "x", Type: "bogus"},
		},
		Routes: []Route{
			{OrgID: "org-a", EnvID: "prod", Position: 0, Matchers: map[string]string{"severity": "critical"}, Receivers: []string{"oncall", "mail"}},
			{OrgID: "org-a", EnvID: "prod", Position: 1, Matchers: map[string]string{"severity": "warning"}, Receivers: []string{"inapp"}},
			{OrgID: "org-b", EnvID: "prod", Position: 0, Matchers: map[string]string{"severity": "critical"}, Receivers: []string{"oncall"}},
			{OrgID: "org-b", EnvID: "prod", Position: 1, Receivers: []string{"hook"}},
		},
	}
}

func mustTree(t *testing.T, r Rendered) *dispatch.Route {
	t.Helper()
	cfg, err := config.Load(string(r.YAML))
	if err != nil {
		t.Fatalf("Alertmanager refuses the rendered config: %v\n%s", err, r.YAML)
	}
	return dispatch.NewRoute(cfg.Route, nil)
}

func receiversFor(tree *dispatch.Route, ls model.LabelSet) []string {
	var out []string
	for _, rt := range tree.Match(ls) {
		out = append(out, rt.RouteOpts.Receiver)
	}
	sort.Strings(out)
	return out
}

func TestRenderedConfigLoadsInAlertmanager(t *testing.T) {
	r, err := Render(fixture())
	if err != nil {
		t.Fatal(err)
	}
	mustTree(t, r)
	if len(r.Skipped) != 1 || !strings.Contains(r.Skipped[0], "org-c-no-recv") {
		t.Fatalf("skipped = %v", r.Skipped)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	a, _ := Render(fixture())
	in := fixture()
	for i, j := 0, len(in.Receivers)-1; i < j; i, j = i+1, j-1 {
		in.Receivers[i], in.Receivers[j] = in.Receivers[j], in.Receivers[i]
	}
	b, _ := Render(in)
	if a.Hash != b.Hash {
		t.Fatalf("hash depends on input order:\n%s\n---\n%s", a.YAML, b.YAML)
	}
}

func TestUnknownOrgAndMissingOrgGoToTheBlackhole(t *testing.T) {
	r, _ := Render(fixture())
	tree := mustTree(t, r)
	for name, ls := range map[string]model.LabelSet{
		"unknown org":     {"reliant_org_id": "org-z", "alertname": "x", "severity": "critical"},
		"no org label":    {"alertname": "x", "severity": "critical"},
		"empty org":       {"reliant_org_id": "", "alertname": "x"},
		"org with none":   {"reliant_org_id": "org-c-no-recv", "alertname": "x"},
		"prefix of org":   {"reliant_org_id": "org-", "alertname": "x"},
		"org + suffix":    {"reliant_org_id": "org-a-evil", "alertname": "x", "severity": "critical"},
		"regex-looking":   {"reliant_org_id": "org-.*", "alertname": "x"},
		"org-a uppercase": {"reliant_org_id": "ORG-A", "alertname": "x", "severity": "critical"},
	} {
		if got := receiversFor(tree, ls); len(got) != 1 || got[0] != Blackhole {
			t.Errorf("%s: delivered to %v, want only the blackhole", name, got)
		}
	}
}

func TestEnvRoutesAndMultiReceiverRouting(t *testing.T) {
	r, _ := Render(fixture())
	tree := mustTree(t, r)
	crit := model.LabelSet{"reliant_org_id": "org-a", "reliant_env_id": "prod", "severity": "critical", "alertname": "x"}
	if got := receiversFor(tree, crit); strings.Join(got, ",") != "org-a/mail,org-a/oncall" {
		t.Fatalf("critical -> %v", got)
	}
	warn := model.LabelSet{"reliant_org_id": "org-a", "reliant_env_id": "prod", "severity": "warning", "alertname": "x"}
	if got := receiversFor(tree, warn); strings.Join(got, ",") != "org-a/inapp" {
		t.Fatalf("warning -> %v", got)
	}
	// no env route matches: the org's default is its in-app receiver.
	info := model.LabelSet{"reliant_org_id": "org-a", "reliant_env_id": "prod", "severity": "info", "alertname": "x"}
	if got := receiversFor(tree, info); strings.Join(got, ",") != "org-a/inapp" {
		t.Fatalf("unmatched -> %v, want the org's in-app default", got)
	}
	// a different env does not use prod's routes.
	other := model.LabelSet{"reliant_org_id": "org-a", "reliant_env_id": "staging", "severity": "critical", "alertname": "x"}
	if got := receiversFor(tree, other); strings.Join(got, ",") != "org-a/inapp" {
		t.Fatalf("other env -> %v", got)
	}
}

// THE ORG-ISOLATION PROPERTY. For every org, and every label set an alert of
// that org could carry (including labels crafted to look like another org's
// routes and receivers), no route of the real Alertmanager routing tree
// delivers to a receiver that belongs to another org.
//
// It runs over the plain fixture and over one where both orgs also have
// autopilot receivers (continue: true routes at the top of each org's subtree,
// scoped by env), so an autopilot route can never widen an org's reach either.
func TestNoRouteDeliversAcrossOrgs(t *testing.T) {
	t.Run("plain", func(t *testing.T) { assertNoCrossOrgDelivery(t, fixture()) })
	t.Run("with autopilot receivers", func(t *testing.T) { assertNoCrossOrgDelivery(t, autopilotFixture()) })
}

func assertNoCrossOrgDelivery(t *testing.T, in Input) {
	r, _ := Render(in)
	tree := mustTree(t, r)

	owner := map[string]string{} // receiver name -> org
	for _, rc := range in.Receivers {
		owner[RecvName(rc.OrgID, rc.Name)] = rc.OrgID
	}
	orgs := []string{"org-a", "org-b", "org-c-no-recv", "org-z", ""}
	envs := []string{"prod", "staging", "", "org-b"}
	severities := []string{"critical", "warning", "info", "error", "", "org-b/oncall", "blackhole"}
	extras := []map[string]string{
		nil,
		{"alertname": "NewIssue"},
		{"alertname": "IssueRegressed", "severity": "info"},
		{TestLabel: "autopilot-b1"},
		{TestLabel: "autopilot-a1"},
		{"receiver": "org-b/oncall"},
		{"reliant_org_id_x": "org-b"},
		{"team": "org-b", "service": "org-b/hook"},
		{"alertname": "org-b/oncall"},
		{TestLabel: "oncall"},
		{TestLabel: "hook"},
		{TestLabel: "org-b/oncall"},
		{TestLabel: "inapp", "severity": "critical"},
	}

	checked := 0
	for _, org := range orgs {
		for _, env := range envs {
			for _, sev := range severities {
				for _, ex := range extras {
					ls := model.LabelSet{"alertname": "x"}
					if org != "" {
						ls["reliant_org_id"] = model.LabelValue(org)
					}
					if env != "" {
						ls["reliant_env_id"] = model.LabelValue(env)
					}
					if sev != "" {
						ls["severity"] = model.LabelValue(sev)
					}
					for k, v := range ex {
						ls[model.LabelName(k)] = model.LabelValue(v)
					}
					for _, got := range receiversFor(tree, ls) {
						checked++
						if got == Blackhole {
							continue
						}
						if o, ok := owner[got]; !ok || o != org {
							t.Fatalf("alert of org %q (%v) was routed to %q, which belongs to %q", org, ls, got, o)
						}
					}
				}
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d deliveries were checked; the property is not being exercised", checked)
	}
}

func TestEnvRouteCannotNameAnotherOrgsReceiver(t *testing.T) {
	in := fixture()
	// org-a's route names "hook", which only org-b has.
	in.Routes = append(in.Routes, Route{OrgID: "org-a", EnvID: "prod", Position: 9, Matchers: map[string]string{"team": "pay"}, Receivers: []string{"hook"}})
	r, _ := Render(in)
	tree := mustTree(t, r)
	if !containsLine(r.Skipped, `receiver "hook" does not exist in this org`) {
		t.Fatalf("the cross-org route was not reported: %v", r.Skipped)
	}
	ls := model.LabelSet{"reliant_org_id": "org-a", "reliant_env_id": "prod", "team": "pay", "alertname": "x"}
	for _, got := range receiversFor(tree, ls) {
		if strings.HasPrefix(got, "org-b/") {
			t.Fatalf("delivered to %s", got)
		}
	}
}

func TestEnvRouteCannotMatchOrSetReservedLabels(t *testing.T) {
	in := fixture()
	in.Routes = []Route{
		{OrgID: "org-a", EnvID: "prod", Position: 0, Matchers: map[string]string{"reliant_org_id": "org-b"}, Receivers: []string{"oncall"}},
		{OrgID: "org-a", EnvID: "prod", Position: 1, Matchers: map[string]string{"Reliant_Env_Id": "x"}, Receivers: []string{"oncall"}},
		{OrgID: "org-a", EnvID: "prod", Position: 2, Matchers: map[string]string{"bad name": "x"}, Receivers: []string{"oncall"}},
	}
	r, _ := Render(in)
	if len(r.Skipped) < 3 {
		t.Fatalf("skipped = %v", r.Skipped)
	}
	mustTree(t, r)
}

func TestMatcherValuesAreQuoted(t *testing.T) {
	in := fixture()
	in.Routes = []Route{{OrgID: "org-a", EnvID: "prod", Matchers: map[string]string{"team": `x", reliant_org_id="org-b`}, Receivers: []string{"oncall"}}}
	r, _ := Render(in)
	tree := mustTree(t, r)
	ls := model.LabelSet{"reliant_org_id": "org-b", "reliant_env_id": "prod", "team": "x", "alertname": "x"}
	for _, got := range receiversFor(tree, ls) {
		if strings.HasPrefix(got, "org-a/") {
			t.Fatalf("a matcher value escaped its quotes and rerouted org-b to %s\n%s", got, r.YAML)
		}
	}
	// the crafted value is just a (never-matching) literal for org-a.
	crafted := model.LabelSet{"reliant_org_id": "org-a", "reliant_env_id": "prod", "team": `x", reliant_org_id="org-b`, "alertname": "x"}
	if got := receiversFor(tree, crafted); len(got) != 1 || got[0] != "org-a/oncall" {
		t.Fatalf("got %v", got)
	}
}

func TestSecretsAreFilesNeverValues(t *testing.T) {
	r, _ := Render(fixture())
	y := string(r.YAML)
	for _, want := range []string{
		"api_url_file: " + SecretDir + "/org-a.oncall",
		"routing_key_file: " + SecretDir + "/org-b.oncall",
		"credentials_file: " + SecretDir + "/org-b.hook",
		"auth_password_file: " + SecretDir + "/smtp-password",
		"smarthost: smtp.resend.com:587",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("config lacks %q:\n%s", want, y)
		}
	}
	for _, bad := range []string{"api_url:", "routing_key:", "auth_password:", "credentials:", "auth_secret"} {
		if strings.Contains(y, bad) {
			t.Errorf("config carries an inline secret field %q", bad)
		}
	}
	var names []string
	for _, f := range r.Files {
		names = append(names, f.OrgID+":"+f.Name)
	}
	if strings.Join(names, ",") != "org-a:org-a.oncall,org-b:org-b.hook,org-b:org-b.oncall" {
		t.Fatalf("files = %v", names)
	}
}

func TestBadReceiversAreSkippedNotFatal(t *testing.T) {
	in := Input{Settings: Settings{}, Receivers: []Receiver{
		{OrgID: "o", Name: "slack-no-secret", Type: TypeSlack},
		{OrgID: "o", Name: "http", Type: TypeWebhook, Settings: map[string]string{"url": "http://insecure.test"}},
		{OrgID: "o", Name: "mail", Type: TypeEmail, Settings: map[string]string{"to": "x@y.test"}},
		{OrgID: "o", Name: "inapp", Type: TypeInApp},
		{OrgID: "o", Name: "../etc", Type: TypeInApp},
		{OrgID: "bad org!", Name: "n", Type: TypeInApp},
		{OrgID: "good", Name: "pd", Type: TypePagerDuty, SecretRef: "good.pd"},
	}}
	r, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Skipped) != 6 {
		t.Fatalf("skipped %d: %v", len(r.Skipped), r.Skipped)
	}
	tree := mustTree(t, r)
	if got := receiversFor(tree, model.LabelSet{"reliant_org_id": "o", "alertname": "x"}); got[0] != Blackhole {
		t.Fatalf("an org with no usable receiver must get the blackhole: %v", got)
	}
	if got := receiversFor(tree, model.LabelSet{"reliant_org_id": "good", "alertname": "x"}); got[0] != "good/pd" {
		t.Fatalf("one org's bad receivers broke another's: %v", got)
	}
}

// A plain-http webhook is refused unless the renderer is a local stack's: in a
// renderer serving several orgs it would let a customer aim Alertmanager at a
// cluster-internal service.
func TestHTTPWebhooksOnlyWhenAllowed(t *testing.T) {
	in := Input{Receivers: []Receiver{
		{OrgID: "o", Name: "hook", Type: TypeWebhook, Settings: map[string]string{"url": "http://host.docker.internal:9999/hook"}},
	}}
	r, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Skipped) != 1 || !strings.Contains(r.Skipped[0], "https") {
		t.Fatalf("http webhook without AllowHTTPWebhooks: skipped = %v, want it refused", r.Skipped)
	}

	in.Settings.AllowHTTPWebhooks = true
	r, err = Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Skipped) != 0 {
		t.Fatalf("http webhook with AllowHTTPWebhooks: skipped = %v", r.Skipped)
	}
	mustTree(t, r)
	if !strings.Contains(string(r.YAML), "http://host.docker.internal:9999/hook") {
		t.Fatalf("the http url did not render:\n%s", r.YAML)
	}

	// The knob widens the scheme to http, nothing else.
	in.Receivers[0].Settings["url"] = "ftp://x.test/hook"
	if r, _ = Render(in); len(r.Skipped) != 1 {
		t.Fatalf("ftp webhook: skipped = %v, want it refused", r.Skipped)
	}
}

func TestEmptyInputIsAValidBlackholeOnlyConfig(t *testing.T) {
	r, err := Render(Input{})
	if err != nil {
		t.Fatal(err)
	}
	tree := mustTree(t, r)
	if got := receiversFor(tree, model.LabelSet{"reliant_org_id": "anyone"}); fmt.Sprint(got) != "["+Blackhole+"]" {
		t.Fatalf("got %v", got)
	}
}

func TestEmailDisabledWithoutSMTP(t *testing.T) {
	in := fixture()
	in.Settings.SMTP = SMTP{}
	r, _ := Render(in)
	if !containsLine(r.Skipped, "email is not configured") {
		t.Fatalf("skipped = %v", r.Skipped)
	}
	if strings.Contains(string(r.YAML), "email_configs") {
		t.Fatal("a half-configured email receiver was rendered")
	}
}

func containsLine(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func TestTestLabelReachesExactlyOneReceiverOfTheSameOrg(t *testing.T) {
	r, _ := Render(fixture())
	tree := mustTree(t, r)
	for _, c := range []struct{ org, recv string }{{"org-a", "oncall"}, {"org-a", "mail"}, {"org-a", "inapp"}, {"org-b", "oncall"}, {"org-b", "hook"}} {
		ls := model.LabelSet{"reliant_org_id": model.LabelValue(c.org), "reliant_env_id": "prod", "alertname": "ReliantTestAlert", "severity": "critical", TestLabel: model.LabelValue(c.recv)}
		if got := receiversFor(tree, ls); len(got) != 1 || got[0] != RecvName(c.org, c.recv) {
			t.Errorf("%s/%s test alert -> %v, want only %s", c.org, c.recv, got, RecvName(c.org, c.recv))
		}
	}
	// A name the org does not have is not a way into another org's receiver.
	ls := model.LabelSet{"reliant_org_id": "org-a", "alertname": "ReliantTestAlert", TestLabel: "hook"}
	for _, got := range receiversFor(tree, ls) {
		if strings.HasPrefix(got, "org-b/") {
			t.Fatalf("org-a test alert named after org-b's receiver went to %s", got)
		}
	}
}

func TestRulesCannotMatchTheTestLabel(t *testing.T) {
	in := fixture()
	in.Routes = append(in.Routes, Route{OrgID: "org-a", EnvID: "prod", Position: 9, Matchers: map[string]string{TestLabel: "oncall"}, Receivers: []string{"oncall"}})
	r, _ := Render(in)
	for _, s := range r.Skipped {
		if strings.Contains(s, TestLabel) {
			return
		}
	}
	t.Fatalf("a route matching %s was accepted: %v", TestLabel, r.Skipped)
}

func TestOrgDefaultIsThePlatformInAppReceiverWhateverTheOrgNamesItsOwn(t *testing.T) {
	in := Input{Settings: baseSettings(), Receivers: []Receiver{
		{OrgID: "org-a", Name: "aaa-inapp", Type: TypeInApp},
		{OrgID: "org-a", Name: DefaultReceiverName, Type: TypeInApp},
		{OrgID: "org-a", Name: "zz", Type: TypeInApp},
	}}
	r, _ := Render(in)
	got := receiversFor(mustTree(t, r), model.LabelSet{"reliant_org_id": "org-a", "alertname": "x"})
	if len(got) != 1 || got[0] != "org-a/"+DefaultReceiverName {
		t.Fatalf("unrouted alert -> %v, want the platform in-app receiver", got)
	}
	// without it, any in-app receiver of the org's, then the first receiver.
	in.Receivers = in.Receivers[:1]
	in.Receivers = append(in.Receivers, Receiver{OrgID: "org-a", Name: "0-hook", Type: TypeWebhook, Settings: map[string]string{"url": "https://x.test/h"}})
	r, _ = Render(in)
	if got := receiversFor(mustTree(t, r), model.LabelSet{"reliant_org_id": "org-a", "alertname": "x"}); len(got) != 1 || got[0] != "org-a/aaa-inapp" {
		t.Fatalf("no platform receiver -> %v, want the org's in-app one", got)
	}
}
