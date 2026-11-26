package obsbackend

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
)

const fullIssueID = "484169c0-ab61-4c24-baf4-889e8d3e3046"

// prefixIssuesCP serves GetIssue for full ids only, as the control plane
// does, and lists the env's issues.
type prefixIssuesCP struct {
	ids  []string
	gets []string
	// endless answers every ListIssues page with a next token, as an env
	// with more issues than the scan reads.
	endless bool
}

func (p *prefixIssuesCP) Call(_ context.Context, proc string, req, out any) error {
	switch {
	case strings.HasSuffix(proc, "/GetIssue"):
		b, _ := json.Marshal(req)
		var in struct {
			IssueID string `json:"issueId"`
		}
		_ = json.Unmarshal(b, &in)
		p.gets = append(p.gets, in.IssueID)
		for _, id := range p.ids {
			if id == in.IssueID {
				return json.Unmarshal([]byte(`{"issue":{"id":"`+id+`","envId":"env-1","status":"ISSUE_STATUS_NEW"}}`), out)
			}
		}
		return &cloud.Error{Code: cloud.CodeNotFound, Message: "issue not found"}
	case strings.HasSuffix(proc, "/ListIssues"):
		var rows []string
		for _, id := range p.ids {
			rows = append(rows, `{"id":"`+id+`","envId":"env-1"}`)
		}
		next := ""
		if p.endless {
			next = "more"
		}
		return json.Unmarshal([]byte(`{"issues":[`+strings.Join(rows, ",")+`],"nextPageToken":"`+next+`"}`), out)
	}
	return &cloud.Error{Code: cloud.CodeUnimplemented, Message: proc}
}

func prefixNarrow() Narrow {
	to := time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)
	return Narrow{Range: Range{From: to.Add(-72 * time.Hour), To: to}}
}

// `forge obs issue 484169c0 prod`: the 8-character id a table shows resolves
// to the one issue it prefixes (obs d42c4b4c).
func TestCloudGetIssueResolvesAUniquePrefix(t *testing.T) {
	cp := &prefixIssuesCP{ids: []string{fullIssueID, "99999999-0000-4000-8000-000000000000"}}
	res, err := Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: cp}.GetIssue(context.Background(), GetIssueRequest{Narrow: prefixNarrow(), ID: "484169c0"})
	if err != nil || res.Issue.ID != fullIssueID {
		t.Fatalf("GetIssue(prefix) = %+v, %v; want the full issue", res.Issue, err)
	}
	if got := cp.gets[len(cp.gets)-1]; got != fullIssueID {
		t.Errorf("last GetIssue asked for %q, want the full id", got)
	}
}

func TestCloudGetIssuePrefixRefusals(t *testing.T) {
	cases := map[string]struct {
		ids  []string
		id   string
		want string
	}{
		"no match":         {[]string{fullIssueID}, "deadbeef", "use the full id from `forge obs issues prod`"},
		"ambiguous prefix": {[]string{fullIssueID, "48416900-0000-4000-8000-000000000000"}, "4841", "give more of the id"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: &prefixIssuesCP{ids: tc.ids}}.GetIssue(context.Background(), GetIssueRequest{Narrow: prefixNarrow(), ID: tc.id})
			if err == nil || !strings.Contains(err.Error(), tc.want) || exitOf(err) != ExitWrong {
				t.Fatalf("err = %v, want a refusal containing %q", err, tc.want)
			}
		})
	}
	// A scan that hits its page cap with issues unread proves no uniqueness:
	// one match so far is refused, not resolved.
	_, err := Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: &prefixIssuesCP{ids: []string{fullIssueID}, endless: true}}.GetIssue(context.Background(), GetIssueRequest{Narrow: prefixNarrow(), ID: "484169c0"})
	if err == nil || !strings.Contains(err.Error(), "could not be resolved to exactly one") || exitOf(err) != ExitWrong {
		t.Errorf("capped scan = %v, want a refusal", err)
	}
	// A full id that is not found stays the control plane's NotFound.
	_, err = Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: &prefixIssuesCP{}}.GetIssue(context.Background(), GetIssueRequest{Narrow: prefixNarrow(), ID: fullIssueID})
	if !isCloudNotFound(err) {
		t.Errorf("full-id miss = %v, want the NotFound unchanged", err)
	}
}
