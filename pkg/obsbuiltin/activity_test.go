package obsbuiltin

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// A project's declared user-activity rules reach every user count: each
// place a panel reads obsquery.UserActivityEvidence it reads the rules' evidence,
// the Overview's Users card included, and a project with no rules is drawn
// exactly as before.
func TestActivityRulesReachEveryUserCount(t *testing.T) {
	rules := obsquery.Activity{NotActivity: []string{"billing_subscription_state"}, ExcludeUsersWith: []string{"internal_user"}}
	evidence, err := obsquery.ActiveEvidenceFor("product_events", rules)
	if err != nil {
		t.Fatal(err)
	}
	in := fullInput()
	in.Activity = rules
	plain := map[string]dashspec.Dashboard{}
	for _, d := range every(fullInput()) {
		plain[d.ID] = d
	}
	counted := map[string]int{}
	for _, d := range every(in) {
		for i, p := range d.Panels {
			for _, sql := range []string{p.SQL, p.TrendSQL} {
				bare, ruled := strings.Count(sql, obsquery.UserActivityEvidence), strings.Count(sql, evidence)
				if bare != ruled {
					t.Errorf("%s/%s reads UserActivityEvidence %d times but the rules only %d", d.ID, p.ID, bare, ruled)
				}
				counted[d.ID] += ruled
			}
			if was := plain[d.ID].Panels[i]; !strings.Contains(was.SQL+was.TrendSQL, obsquery.UserActivityEvidence) &&
				(was.SQL != p.SQL || was.TrendSQL != p.TrendSQL) {
				t.Errorf("%s/%s does not count users but the rules changed it", d.ID, p.ID)
			}
		}
	}
	for _, id := range []string{UsersID, "overview"} {
		if counted[id] == 0 {
			t.Errorf("no panel of %s reads the rules", id)
		}
	}
}

func TestNoActivityRulesDrawTheDashboardsUnchanged(t *testing.T) {
	a, b := every(fullInput()), every(func() Input { in := fullInput(); in.Activity = obsquery.Activity{}; return in }())
	for i := range a {
		if string(marshal(t, a[i])) != string(marshal(t, b[i])) {
			t.Errorf("%s changed under zero rules", a[i].ID)
		}
	}
}

// The Overview's Users card counts users in four panels (active, new,
// returning, daily active) and falls back to users seen for Accounts. Each
// builds its SQL from fragments (the active const, newUsers) rather than
// naming obsquery.UserActivityEvidence itself, so this checks the result, not the
// token: every statement of the card that reads UserId carries the project's
// exclusion and not-activity rules.
func TestOverviewUserCardHonoursActivityRules(t *testing.T) {
	in := fullInput()
	in.Activity = obsquery.Activity{NotActivity: []string{"billing_subscription_state"}, ExcludeUsersWith: []string{"internal_user"}}
	const exclude = "(EnvId, UserId) NOT IN (SELECT EnvId, UserId FROM product_events WHERE EventName IN ('internal_user') AND UserId != '')"
	const notActivity = "EventName NOT IN ('billing_subscription_state')"
	var overview *dashspec.Dashboard
	for _, d := range every(in) {
		if d.ID == "overview" {
			overview = &d
			break
		}
	}
	if overview == nil {
		t.Fatal("no overview dashboard")
	}
	want := map[string]bool{"active-7d": true, "new-7d": true, "returning-7d": true, "dau-30d": true, "accounts": true}
	for _, p := range overview.Panels {
		if p.Section != "users" {
			continue
		}
		delete(want, p.ID)
		for kind, sql := range map[string]string{"SQL": p.SQL, "TrendSQL": p.TrendSQL} {
			if !strings.Contains(sql, "UserId") {
				continue
			}
			if !strings.Contains(sql, exclude) || !strings.Contains(sql, notActivity) {
				t.Errorf("overview/%s %s counts users without the project's rules:\n%s", p.ID, kind, sql)
			}
		}
	}
	if len(want) > 0 {
		t.Errorf("the Users card no longer has panels %v; update this test", want)
	}
}

// Every sign-up cohort reads the rules too: the Acquisition's week-4
// retention counts its eligible sign-ups from suCTE, and a marked user's
// sign-up must leave the cohort, not only its week-4 activity.
func TestSignupCohortsHonourActivityRules(t *testing.T) {
	in := fullInput()
	in.Activity = obsquery.Activity{ExcludeUsersWith: []string{"internal_user"}}
	const exclude = "(EnvId, UserId) NOT IN (SELECT EnvId, UserId FROM product_events WHERE EventName IN ('internal_user') AND UserId != '')"
	seen := 0
	for _, d := range every(in) {
		for _, p := range d.Panels {
			for _, sql := range []string{p.SQL, p.TrendSQL} {
				i := strings.Index(sql, "su AS (")
				if i < 0 {
					continue
				}
				seen++
				end := strings.Index(sql[i:], "GROUP BY EnvId, UserId)")
				if end < 0 || !strings.Contains(sql[i:i+end], exclude) {
					t.Errorf("%s/%s: its sign-up cohort (su) does not leave marked users out", d.ID, p.ID)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("no panel builds a sign-up cohort; the scan is broken")
	}
}
