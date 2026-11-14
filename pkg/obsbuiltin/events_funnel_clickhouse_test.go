package obsbuiltin

import (
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// Events funnels (forge.Step, by = "org", within) and counts, run against
// ClickHouse. Orgs, over two environments (a CLI's and the product's):
//
//	o1  first deploy 03-01, claimed 03-01, second deploy 03-05: every step.
//	o2  first deploy 03-02, claimed, second deploy 03-25: the last step is
//	    20 days after the claim, outside its 14d `within`.
//	o3  first deploy 03-03, never claimed.
//	o4  only a repeat deploy (first_for_org=false): never enters the funnel.
const eventsSeed = `
INSERT INTO forge_obsbuiltin_events.product_events (Timestamp, ServiceName, EnvId, OrgId, UserId, EventName, Props, Client) VALUES
    ('2026-03-01 10:00:00', 'api', 'e1', 'o1', 'u1', 'deploy_succeeded', {'mode': 'claimable', 'first_for_org': 'true'}, 'backend'),
    ('2026-03-01 11:00:00', 'api', 'e1', 'o1', 'u1', 'claim_completed', {'path': 'upgrade'}, 'backend'),
    ('2026-03-05 09:00:00', 'cli', 'e2', 'o1', 'u1', 'deploy_succeeded', {'mode': 'account', 'first_for_org': 'false'}, 'cli'),
    ('2026-03-02 10:00:00', 'api', 'e1', 'o2', '',   'deploy_succeeded', {'mode': 'claimable', 'first_for_org': 'true'}, 'backend'),
    ('2026-03-02 12:00:00', 'api', 'e1', 'o2', 'u2', 'claim_completed', {'path': 'upgrade'}, 'backend'),
    ('2026-03-25 12:00:00', 'api', 'e1', 'o2', 'u2', 'deploy_succeeded', {'mode': 'account', 'first_for_org': 'false'}, 'backend'),
    ('2026-03-03 10:00:00', 'api', 'e1', 'o3', '',   'deploy_succeeded', {'mode': 'claimable', 'first_for_org': 'true'}, 'backend'),
    ('2026-03-04 10:00:00', 'api', 'e1', 'o4', 'u4', 'deploy_succeeded', {'mode': 'account', 'first_for_org': 'false'}, 'backend');
`

func eventsFunnel() Funnel {
	return Funnel{
		ID: "forge-deploy", Title: "First deploy to second deploy",
		Funnel: dashspec.Funnel{Steps: []string{"deploy_succeeded", "claim_completed", "deploy_succeeded"}, Window: "30d", By: FunnelByOrg},
		StepFilters: []FunnelStep{
			{Where: map[string]string{"first_for_org": "true"}},
			{},
			{Where: map[string]string{"first_for_org": "false"}, Within: "14d"},
		},
	}
}

func TestEventsFunnelByOrgWithStepFiltersAndWithin(t *testing.T) {
	ch := newClickHouse(t, "forge_obsbuiltin_events", eventsSeed)
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	p := productFunnel(eventsFunnel())
	want := "deploy_succeeded (first_for_org=true)\t3\nclaim_completed\t2\ndeploy_succeeded (first_for_org=false)\t1"
	if got := ch.run(p.SQL, start, end, "e1", "e2"); got != want {
		t.Errorf("funnel across both envs:\n%s\nwant\n%s", got, want)
	}
	// One environment only: o1's second deploy was the CLI env's.
	if got := ch.run(p.SQL, start, end, "e1"); got != "deploy_succeeded (first_for_org=true)\t3\nclaim_completed\t2\ndeploy_succeeded (first_for_org=false)\t0" {
		t.Errorf("funnel over e1 only:\n%s", got)
	}

	// Without `within`, windowFunnel: o2's repeat is inside the 30d window.
	plain := eventsFunnel()
	plain.StepFilters[2].Within = ""
	if got := ch.run(productFunnel(plain).SQL, start, end, "e1", "e2"); got != "deploy_succeeded (first_for_org=true)\t3\nclaim_completed\t2\ndeploy_succeeded (first_for_org=false)\t2" {
		t.Errorf("windowFunnel:\n%s", got)
	}

	c := countPanel(Count{ID: "deploys", Event: "deploy_succeeded", GroupBy: []string{"mode", "first_for_org"}})
	wantCount := "claimable\ttrue\t3\t3\t1\naccount\tfalse\t3\t3\t3"
	if got := ch.run(c.SQL, start, end, "e1", "e2"); got != wantCount {
		t.Errorf("count:\n%s\nwant\n%s", got, wantCount)
	}
}

// Steps in order with other step events between them still count: o5 repeats a
// deploy before its claim and claims twice before its second deploy. A
// sequenceMatch pattern of bare (?1)(?2) would need the steps adjacent among
// the described events; each (?t<=N) between steps lets any number of events
// lie between them, so o5 reaches every step.
const interleavedSeed = `
INSERT INTO forge_obsbuiltin_events_interleaved.product_events (Timestamp, ServiceName, EnvId, OrgId, UserId, EventName, Props, Client) VALUES
    ('2026-03-06 10:00:00', 'api', 'e1', 'o5', 'u5', 'deploy_succeeded', {'mode': 'claimable', 'first_for_org': 'true'}, 'backend'),
    ('2026-03-06 10:30:00', 'api', 'e1', 'o5', 'u5', 'deploy_succeeded', {'mode': 'claimable', 'first_for_org': 'false'}, 'backend'),
    ('2026-03-06 11:00:00', 'api', 'e1', 'o5', 'u5', 'claim_completed', {'path': 'upgrade'}, 'backend'),
    ('2026-03-06 11:30:00', 'api', 'e1', 'o5', 'u5', 'claim_completed', {'path': 'membership'}, 'backend'),
    ('2026-03-07 09:00:00', 'api', 'e1', 'o5', 'u5', 'deploy_succeeded', {'mode': 'account', 'first_for_org': 'false'}, 'backend');
`

func TestEventsFunnelCountsStepsWithOtherStepEventsBetweenThem(t *testing.T) {
	ch := newClickHouse(t, "forge_obsbuiltin_events_interleaved", interleavedSeed)
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	want := "deploy_succeeded (first_for_org=true)\t1\nclaim_completed\t1\ndeploy_succeeded (first_for_org=false)\t1"
	if got := ch.run(productFunnel(eventsFunnel()).SQL, start, end, "e1"); got != want {
		t.Errorf("sequenced funnel with interleaved step events:\n%s\nwant\n%s", got, want)
	}
}
