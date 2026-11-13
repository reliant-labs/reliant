package obsbuiltin

import (
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

func fullInput() Input {
	return Input{Project: "shop", Web: true,
		Analytics: &Analytics{
			Goals:      []Goal{{ID: "checkout", Title: "Checkout", Event: "purchase"}, {ID: "pricing", Route: "/pricing"}},
			Activation: "order_created", ActivationWindow: "7d",
			Channels: []ChannelRule{{Host: "news.ycombinator.com", Channel: "Hacker News"}, {Source: "partner", Medium: "referral", Channel: "Partners"}},
		},
		Funnels: []Funnel{
			{ID: "buy", Title: "Buy", Funnel: dashspec.Funnel{Steps: []string{"page_viewed", "add_to_cart", "purchase"}, By: FunnelByPerson, Window: "1d"}},
			{ID: "browse", Funnel: dashspec.Funnel{Steps: []string{"page_viewed", "add_to_cart"}, By: FunnelByVisitor}},
		}}
}

func TestEveryBuiltinPanelRunsOnClickHouse(t *testing.T) {
	ch := newClickHouse(t, "forge_obsbuiltin_smoke", "")
	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	in := fullInput()
	ds := Dashboards(in)
	ds = append(ds, ExtendOverview(dashspec.Dashboard{ID: "overview", Title: "o"}, in))
	for _, d := range ds {
		for _, p := range d.Panels {
			if _, err := ch.try(p.SQL, end.Add(-30*24*time.Hour), end, "e1"); err != nil {
				t.Errorf("%s/%s: %v", d.ID, p.ID, err)
			}
			if p.TrendSQL != "" {
				if _, err := ch.try(p.TrendSQL, end.Add(-30*24*time.Hour), end, "e1"); err != nil {
					t.Errorf("%s/%s trend: %v", d.ID, p.ID, err)
				}
			}
		}
	}
}
