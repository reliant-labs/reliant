package obsbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// pagedActivity serves ListActivity as pages of size events, newest first,
// total events in all, and records every page token it was asked for.
type pagedActivity struct {
	size, total int
	tokens      []string
}

func (p *pagedActivity) Call(_ context.Context, procedure string, req, out any) error {
	if procedure != procListActivity {
		return fmt.Errorf("unexpected %s", procedure)
	}
	body := req.(map[string]any)
	tok, _ := body["pageToken"].(string)
	p.tokens = append(p.tokens, tok)
	start := 0
	if tok != "" {
		_, _ = fmt.Sscanf(tok, "p%d", &start)
	}
	var page struct {
		Events        []wireActivityEvent `json:"events"`
		NextPageToken string              `json:"nextPageToken"`
	}
	for i := start; i < start+p.size && i < p.total; i++ {
		page.Events = append(page.Events, wireActivityEvent{At: t0c.Add(-time.Duration(i) * time.Minute), Verb: "restart"})
	}
	if start+p.size < p.total {
		page.NextPageToken = fmt.Sprintf("p%d", start+p.size)
	}
	b, _ := json.Marshal(page)
	return json.Unmarshal(b, out)
}

// forge report 0108cca3: obs changes read one 200-event page of activity and
// reported truncated:false, so older events in the range silently vanished.
func TestActivityPagesFollowsTheTokenAndSaysWhenMoreRemain(t *testing.T) {
	for _, tc := range []struct {
		name               string
		total, limit       int
		wantEvents, wantPg int
		wantMore           bool
	}{
		{"one page is all of it", 150, 1000, 150, 1, false},
		{"pages to the end", 450, 1000, 450, 3, false},
		{"stops once the limit is held", 450, 250, 400, 2, true},
		{"limit 0 reads everything", 450, 0, 450, 3, false},
		{"the page cap is reported, not hidden", changesPageSize * (changesMaxPages + 2), 0, changesPageSize * changesMaxPages, changesMaxPages, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &pagedActivity{size: changesPageSize, total: tc.total}
			c := Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: p}
			events, more, err := c.activityPages(context.Background(), map[string]any{"orgId": "org-1"}, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != tc.wantEvents || len(p.tokens) != tc.wantPg || more != tc.wantMore {
				t.Errorf("events=%d pages=%d more=%v, want %d %d %v", len(events), len(p.tokens), more, tc.wantEvents, tc.wantPg, tc.wantMore)
			}
			if len(p.tokens) > 1 && p.tokens[1] != fmt.Sprintf("p%d", changesPageSize) {
				t.Errorf("second page asked with token %q", p.tokens[1])
			}
		})
	}
}
