// Copyright (c) 2025 Reliant Labs

package amconfig

import (
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/common/model"
)

func autopilotReceiver(org, name string, envs ...string) Receiver {
	return Receiver{
		OrgID: org, Name: name, Type: TypeAutopilot, SecretRef: org + "." + name,
		Settings: map[string]string{"url": "https://reliant.test/hooks/" + name},
		Autopilot: &AutopilotRoute{
			EnvIDs:           envs,
			Severities:       []string{"critical", "error"},
			AlwaysAlertnames: []string{"NewIssue", "IssueRegressed", "IssueSpike"},
		},
	}
}

// autopilotFixture is fixture() plus an autopilot receiver in each org. Both
// are scoped to an env called "prod", which org-a ALSO routes, so an autopilot
// route that leaked across orgs or swallowed the org's own routing would show.
func autopilotFixture() Input {
	in := fixture()
	in.Receivers = append(in.Receivers,
		autopilotReceiver("org-a", "autopilot-a1", "prod"),
		autopilotReceiver("org-b", "autopilot-b1", "prod", "staging"),
	)
	return in
}

func without(rs []string, drop ...string) []string {
	var out []string
	for _, r := range rs {
		if !slices.Contains(drop, r) {
			out = append(out, r)
		}
	}
	return out
}

// THE OBSERVER PROPERTY. Adding an autopilot receiver changes nothing about
// where an org's alerts already went: for every label set, the deliveries with
// it are the deliveries without it, plus the autopilot receiver exactly when
// the alert is in its env scope and passes its filter (a severity it names, or
// one of its alert names whatever the severity). The blackhole is ignored: it
// delivers to no one.
func TestAutopilotObservesWithoutChangingTheOrgsRouting(t *testing.T) {
	base := mustTree(t, mustRender(t, fixture()))
	with := mustTree(t, mustRender(t, autopilotFixture()))

	checked, observed := 0, 0
	for _, org := range []string{"org-a", "org-b", "org-z"} {
		for _, env := range []string{"prod", "staging", "dev", ""} {
			for _, sev := range []string{"critical", "error", "warning", "info", ""} {
				for _, name := range []string{"x", "NewIssue", "IssueRegressed", "IssueSpike", "newissue"} {
					ls := model.LabelSet{"reliant_org_id": model.LabelValue(org), "alertname": model.LabelValue(name)}
					if env != "" {
						ls["reliant_env_id"] = model.LabelValue(env)
					}
					if sev != "" {
						ls["severity"] = model.LabelValue(sev)
					}
					before := without(receiversFor(base, ls), Blackhole)
					after := without(receiversFor(with, ls), Blackhole)

					var want []string
					ap := map[string]string{"org-a": "org-a/autopilot-a1", "org-b": "org-b/autopilot-b1"}[org]
					inScope := (org == "org-a" && env == "prod") || (org == "org-b" && (env == "prod" || env == "staging"))
					passes := sev == "critical" || sev == "error" || name == "NewIssue" || name == "IssueRegressed" || name == "IssueSpike"
					want = append(want, before...)
					if inScope && passes {
						want = append(want, ap)
						observed++
					}
					slices.Sort(want)
					if strings.Join(after, ",") != strings.Join(want, ",") {
						t.Errorf("%v: delivered to %v, want %v (without autopilot: %v)", ls, after, want, before)
					}
					checked++
				}
			}
		}
	}
	if observed < 20 || checked < 200 {
		t.Fatalf("checked %d label sets, %d reached autopilot: the property is not exercised", checked, observed)
	}
}

// An org whose own routing has env routes AND an org with none at all both
// keep their default receiver for alerts autopilot observed.
func TestAutopilotKeepsTheOrgDefaultForWhatItObserves(t *testing.T) {
	in := Input{Settings: baseSettings(), Receivers: []Receiver{
		{OrgID: "o", Name: "reliant-inapp", Type: TypeInApp},
		autopilotReceiver("o", "autopilot-1", "e1"),
	}}
	tree := mustTree(t, mustRender(t, in))
	ls := model.LabelSet{"reliant_org_id": "o", "reliant_env_id": "e1", "severity": "critical", "alertname": "x"}
	if got := without(receiversFor(tree, ls), Blackhole); strings.Join(got, ",") != "o/autopilot-1,o/reliant-inapp" {
		t.Fatalf("critical in scope -> %v, want autopilot AND the in-app default", got)
	}
	ls["severity"] = "warning"
	if got := receiversFor(tree, ls); strings.Join(got, ",") != "blackhole,o/reliant-inapp" {
		t.Fatalf("warning in scope -> %v, want the in-app default only (and the filter's blackhole parent)", got)
	}
}

func TestAutopilotIsNeverTheOrgDefault(t *testing.T) {
	in := Input{Settings: baseSettings(), Receivers: []Receiver{autopilotReceiver("o", "autopilot-1", "e1")}}
	tree := mustTree(t, mustRender(t, in))
	for name, ls := range map[string]model.LabelSet{
		"out of scope":    {"reliant_org_id": "o", "reliant_env_id": "e2", "severity": "critical", "alertname": "x"},
		"filtered out":    {"reliant_org_id": "o", "reliant_env_id": "e1", "severity": "info", "alertname": "x"},
		"no env label":    {"reliant_org_id": "o", "severity": "critical", "alertname": "x"},
		"no severity":     {"reliant_org_id": "o", "reliant_env_id": "e1", "alertname": "x"},
		"near-miss issue": {"reliant_org_id": "o", "reliant_env_id": "e1", "alertname": "NewIssueX"},
	} {
		if got := receiversFor(tree, ls); slices.Contains(got, "o/autopilot-1") {
			t.Errorf("%s: delivered to %v; an alert outside the filter must not reach autopilot", name, got)
		}
	}
}

func TestAutopilotWithNoEnvInScopeGetsNoRouteButStillTests(t *testing.T) {
	in := Input{Settings: baseSettings(), Receivers: []Receiver{
		{OrgID: "o", Name: "reliant-inapp", Type: TypeInApp},
		autopilotReceiver("o", "autopilot-1"),
	}}
	r := mustRender(t, in)
	tree := mustTree(t, r)
	ls := model.LabelSet{"reliant_org_id": "o", "reliant_env_id": "e1", "severity": "critical", "alertname": "x"}
	if got := receiversFor(tree, ls); strings.Join(got, ",") != "o/reliant-inapp" {
		t.Fatalf("-> %v, want the org's routing untouched", got)
	}
	ls[TestLabel] = "autopilot-1"
	if got := receiversFor(tree, ls); strings.Join(got, ",") != "o/autopilot-1" {
		t.Fatalf("test alert -> %v, want exactly the autopilot receiver", got)
	}
	if strings.Contains(string(r.YAML), "continue: true") {
		t.Fatalf("an autopilot receiver with no env in scope rendered a route:\n%s", r.YAML)
	}
}

func TestAutopilotAllSeverities(t *testing.T) {
	rc := autopilotReceiver("o", "autopilot-1", "e1")
	rc.Autopilot.Severities = []string{"*"}
	tree := mustTree(t, mustRender(t, Input{Settings: baseSettings(), Receivers: []Receiver{rc}}))
	for _, sev := range []string{"info", "warning", ""} {
		ls := model.LabelSet{"reliant_org_id": "o", "reliant_env_id": "e1", "alertname": "x"}
		if sev != "" {
			ls["severity"] = model.LabelValue(sev)
		}
		if got := receiversFor(tree, ls); !slices.Contains(got, "o/autopilot-1") {
			t.Errorf("severity %q -> %v, want autopilot", sev, got)
		}
	}
}

// Env ids and filter values are matched LITERALLY: a value with regex
// metacharacters neither widens the match nor breaks the config.
func TestAutopilotMatchersAreLiteral(t *testing.T) {
	rc := autopilotReceiver("o", "autopilot-1", "env.1", "a|b", `x\y`)
	rc.Autopilot.Severities = []string{"crit.*"}
	r := mustRender(t, Input{Settings: baseSettings(), Receivers: []Receiver{rc}})
	tree := mustTree(t, r)
	cases := map[string]bool{"env.1": true, "envX1": false, "a|b": true, "a": false, "b": false, `x\y`: true}
	for env, want := range cases {
		ls := model.LabelSet{"reliant_org_id": "o", "reliant_env_id": model.LabelValue(env), "severity": "crit.*", "alertname": "x"}
		if got := slices.Contains(receiversFor(tree, ls), "o/autopilot-1"); got != want {
			t.Errorf("env %q: reached autopilot = %v, want %v", env, got, want)
		}
	}
	ls := model.LabelSet{"reliant_org_id": "o", "reliant_env_id": "env.1", "severity": "critical", "alertname": "x"}
	if slices.Contains(receiversFor(tree, ls), "o/autopilot-1") {
		t.Errorf(`severity "crit.*" matched "critical": it must be literal`)
	}
}

func TestAutopilotDeliveryIsBearerAuthenticated(t *testing.T) {
	r := mustRender(t, Input{Settings: baseSettings(), Receivers: []Receiver{autopilotReceiver("o", "autopilot-1", "e1")}})
	y := string(r.YAML)
	for _, want := range []string{"https://reliant.test/hooks/autopilot-1", "credentials_file: " + SecretDir + "/o.autopilot-1", "type: Bearer"} {
		if !strings.Contains(y, want) {
			t.Fatalf("config lacks %q:\n%s", want, y)
		}
	}
	if len(r.Files) != 1 || r.Files[0].Name != "o.autopilot-1" || r.Files[0].Receiver != "autopilot-1" {
		t.Fatalf("secret files = %+v", r.Files)
	}
}

func TestAutopilotBadReceiversAreSkipped(t *testing.T) {
	noToken := autopilotReceiver("o", "autopilot-1", "e1")
	noToken.SecretRef = ""
	plainHTTP := autopilotReceiver("o", "autopilot-2", "e1")
	plainHTTP.Settings["url"] = "http://reliant.example.com/hooks/x"
	internalHTTP := autopilotReceiver("o", "autopilot-3", "e1")
	internalHTTP.Settings["url"] = "http://reliant-api-server.reliant.svc.cluster.local:8080/hooks/x"
	r := mustRender(t, Input{Settings: baseSettings(), Receivers: []Receiver{noToken, plainHTTP, internalHTTP}})
	mustTree(t, r)
	if len(r.Skipped) != 2 || !containsLine(r.Skipped, `"autopilot-1"`) || !containsLine(r.Skipped, `"autopilot-2"`) {
		t.Fatalf("skipped = %v, want autopilot-1 (no token) and autopilot-2 (public http)", r.Skipped)
	}
	if !strings.Contains(string(r.YAML), "reliant-api-server.reliant.svc.cluster.local") {
		t.Fatalf("a cluster-internal http URL must render:\n%s", r.YAML)
	}
}

// An org with no autopilot receiver renders exactly as before: enabling one
// re-groups only that org's alerts.
func TestOrgsWithoutAutopilotRenderUnwrapped(t *testing.T) {
	r := mustRender(t, fixture())
	if strings.Contains(string(r.YAML), "continue: true\n      receiver: blackhole") {
		t.Fatalf("an org without autopilot got an autopilot route:\n%s", r.YAML)
	}
	a := mustRender(t, autopilotFixture())
	if a.Hash == r.Hash {
		t.Fatal("adding autopilot receivers did not change the config")
	}
}

func mustRender(t *testing.T, in Input) Rendered {
	t.Helper()
	r, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
