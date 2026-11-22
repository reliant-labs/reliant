package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/suspect"
)

// The culprit file is usually the top frame too; suspects name it once.
func TestSuspectCommitsNameEachFileOnce(t *testing.T) {
	var got []string
	withSuspects(t, func(_ context.Context, _ string, files []string) ([]suspect.Commit, error) {
		got = files
		return nil, suspect.ErrNoFiles
	})
	res := obsbackend.GetIssueResult{Issue: obsbackend.Issue{Culprit: "internal/handlers/catalog/handlers_crud.go in (*Service).GetProduct"},
		Frames: []obsbackend.IssueFrame{{File: "internal/handlers/catalog/handlers_crud.go", InApp: true}, {File: "internal/db/x.go", InApp: true}}}
	var errOut bytes.Buffer
	_, note := suspectCommits(context.Background(), &errOut, res, "")
	if len(got) != 2 || strings.Count(note, "handlers_crud.go") != 1 {
		t.Fatalf("files = %v, note = %q", got, note)
	}
}

// A browser issue is offered the same resolve as a server one.
func TestIssueSuggestionsOfferResolveForBrowserIssues(t *testing.T) {
	res := obsbackend.GetIssueResult{Issue: obsbackend.Issue{ID: "ISS-7f3a", Platform: obsbackend.PlatformBrowser}}
	if s := strings.Join(issueSuggestions("prod", res), "\n"); !strings.Contains(s, "issue resolve ISS-7f3a prod --in next") {
		t.Fatalf("suggested = %q", s)
	}
}

// A resolve says what reopens it.
func TestResolveMeaning(t *testing.T) {
	for _, in := range []string{obsbackend.ResolveNow, obsbackend.ResolveNextRelease} {
		if m := resolveMeaning(obsbackend.UpdateIssueStatusRequest{Status: obsbackend.IssueResolved, ResolveIn: in}); !strings.Contains(m, "promoted after now reopens it") {
			t.Errorf("%v: %q", in, m)
		}
	}
	if m := resolveMeaning(obsbackend.UpdateIssueStatusRequest{Status: obsbackend.IssueIgnored}); m != "" {
		t.Errorf("ignore: %q", m)
	}
}
