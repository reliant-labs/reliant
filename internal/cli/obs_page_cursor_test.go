package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
)

// cloudTestRange is the default 24h window the cloud verb tests run over.
var cloudTestRange = obsbackend.Range{From: obsNow.Add(-24 * time.Hour), To: obsNow}

// The bug: `forge obs issues prod --since 72h --page-token <next_page_token>`
// was refused "page_token belongs to a different request", because --since
// recomputed the window from a new now. A forge-issued token carries its
// window, so resuming pins the range to it whatever --since says now.
func TestResumePagePinsTheTokensWindow(t *testing.T) {
	issued := obsbackend.Range{From: obsNow.Add(-72 * time.Hour), To: obsNow}
	tok := wrapPageToken(issued, "server-tok")
	if !strings.HasPrefix(tok, pageCursorPrefix) {
		t.Fatalf("token %q is not forge-wrapped", tok)
	}
	n := obsbackend.Narrow{Range: obsbackend.Range{From: obsNow.Add(-71 * time.Hour), To: obsNow.Add(time.Hour)}}
	inner, err := resumePage(&n, tok)
	if err != nil {
		t.Fatal(err)
	}
	if inner != "server-tok" || !n.Range.From.Equal(issued.From) || !n.Range.To.Equal(issued.To) {
		t.Fatalf("resumed %q over %v, want server-tok over %v", inner, n.Range, issued)
	}
}

func TestResumePageLeavesForeignTokensAlone(t *testing.T) {
	n := obsbackend.Narrow{Range: cloudTestRange}
	inner, err := resumePage(&n, "tok1")
	if err != nil || inner != "tok1" || n.Range != cloudTestRange {
		t.Fatalf("got %q %v %v", inner, n.Range, err)
	}
	if wrapPageToken(cloudTestRange, "") != "" {
		t.Error("no next page must stay no token")
	}
	if _, err := resumePage(&n, pageCursorPrefix+"!!"); err == nil {
		t.Error("a damaged forge token must be refused, not sent to the backend")
	}
}

// End to end over the cloud fake: the second page is requested over the first
// page's window, not a fresh one.
func TestObsIssuesNextPageResumesTheSameWindow(t *testing.T) {
	issued := obsbackend.Range{From: obsNow.Add(-72 * time.Hour).Add(-time.Minute), To: obsNow.Add(-time.Minute)}
	fc := &fakeCloudCaller{replies: map[string]string{cloudList: `{"issues":[]}`}}
	if _, _, err := obsRunCloud(t, fc, "issues", "prod", "--json", "--since", "72h", "--page-token", wrapPageToken(issued, "tok2")); err != nil {
		t.Fatal(err)
	}
	body := fc.calls[0].body
	for _, want := range []string{`"pageToken":"tok2"`, issued.From.Format(time.RFC3339), issued.To.Format(time.RFC3339)} {
		if !strings.Contains(body, want) {
			t.Errorf("request lacks %s:\n%s", want, body)
		}
	}
}
