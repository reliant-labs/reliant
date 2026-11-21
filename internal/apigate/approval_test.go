package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGitHub answers ghAPI from canned responses: the PR at
// repos/o/r/pulls/5 and its events at repos/o/r/issues/5/events. events holds
// one JSON array per page, printed back to back as `gh api --paginate` does.
type fakeGitHub struct {
	pr        string
	events    []string
	prErr     error
	eventsErr error
	calls     [][]string
}

func (f *fakeGitHub) api(args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	switch strings.Join(args, " ") {
	case "repos/o/r/pulls/5":
		return []byte(f.pr), f.prErr
	case "--paginate repos/o/r/issues/5/events":
		return []byte(strings.Join(f.events, "\n")), f.eventsErr
	}
	return nil, errors.New("fake gh: unexpected call: gh api " + strings.Join(args, " "))
}

// useFakeGitHub routes ghAPI to f for the test, for repository o/r and the
// default owners: reliant-01 and reliant-labs[bot].
func useFakeGitHub(t *testing.T, f *fakeGitHub) {
	t.Helper()
	t.Setenv("GITHUB_REPOSITORY", "o/r")
	t.Setenv("APIGATE_OWNERS", "")
	prev := ghAPI
	ghAPI = f.api
	t.Cleanup(func() { ghAPI = prev })
}

const (
	prApproved   = `{"body":"Drop widget.Close.\n\nAPI-BREAK: codemod=widget-close\n","labels":[{"name":"api-break"},{"name":"breaking:approved"}]}`
	prUnapproved = `{"body":"Drop widget.Close.\n\nAPI-BREAK: codemod=widget-close\n","labels":[{"name":"api-break"}]}`
)

func labeled(actor, label string) string {
	return `{"event":"labeled","label":{"name":"` + label + `"},"actor":{"login":"` + actor + `"}}`
}

func page(events ...string) string { return "[" + strings.Join(events, ",") + "]" }

// The pkg/ API gate's approval attribution: breaking:approved counts only
// while it is on the PR and the last account to apply it is the owner's, or
// the Reliant bot's, which labels on the owner's instruction (owner decision,
// 2026-11-19). No other account's label counts.
func TestPRDeclarationOwnerApproval(t *testing.T) {
	other := `{"event":"commented","actor":{"login":"reliant-01"}}`
	cases := []struct {
		name   string
		pr     string
		events []string
		want   bool
	}{
		{"the owner applied the label", prApproved,
			[]string{page(labeled("reliant-01", ApprovedLabel))}, true},
		{"the Reliant bot applied the label", prApproved,
			[]string{page(labeled("reliant-labs[bot]", ApprovedLabel))}, true},
		{"another bot applied the label", prApproved,
			[]string{page(labeled("github-actions[bot]", ApprovedLabel))}, false},
		{"a non-owner reapplied the Reliant bot's label", prApproved,
			[]string{page(labeled("reliant-labs[bot]", ApprovedLabel), labeled("someone", ApprovedLabel))}, false},
		{"the PR author applied the label", prApproved,
			[]string{page(labeled("someone", ApprovedLabel))}, false},
		{"the owner applied a different label", prApproved,
			[]string{page(labeled("someone", ApprovedLabel), labeled("reliant-01", "api-break"))}, false},
		{"the label is absent", prUnapproved, nil, false},
		{"the owner's label was since removed", prUnapproved,
			[]string{page(labeled("reliant-01", ApprovedLabel), `{"event":"unlabeled","label":{"name":"breaking:approved"},"actor":{"login":"someone"}}`)}, false},
		{"a non-owner reapplied the owner's label", prApproved,
			[]string{page(labeled("reliant-01", ApprovedLabel), labeled("someone", ApprovedLabel))}, false},
		{"the owner's label is on a later page", prApproved,
			[]string{page(other, other, labeled("someone", ApprovedLabel)), page(other, labeled("reliant-01", ApprovedLabel))}, true},
		{"a non-owner reapplied it on a later page", prApproved,
			[]string{page(labeled("reliant-01", ApprovedLabel)), page(other), page(labeled("someone", ApprovedLabel))}, false},
		{"the label carries no labeled event", prApproved, []string{page(other)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGitHub{pr: tc.pr, events: tc.events}
			useFakeGitHub(t, f)
			body, labels, approved, err := prDeclaration("5")
			if err != nil {
				t.Fatal(err)
			}
			if approved != tc.want {
				t.Errorf("approved = %v, want %v (gh calls %v)", approved, tc.want, f.calls)
			}
			if !strings.Contains(body, "API-BREAK: codemod=widget-close") || len(labels) == 0 || labels[0] != "api-break" {
				t.Errorf("body %q, labels %v: want the PR's body and labels", body, labels)
			}
		})
	}
}

func TestPRDeclarationOwnersOverride(t *testing.T) {
	f := &fakeGitHub{pr: prApproved, events: []string{page(labeled("deputy", ApprovedLabel))}}
	useFakeGitHub(t, f)
	t.Setenv("APIGATE_OWNERS", "reliant-01, deputy")
	if _, _, approved, err := prDeclaration("5"); err != nil || !approved {
		t.Fatalf("an account named in APIGATE_OWNERS must count: approved=%v err=%v", approved, err)
	}
	// An override replaces the default: naming only the owner drops the bot.
	f = &fakeGitHub{pr: prApproved, events: []string{page(labeled("reliant-labs[bot]", ApprovedLabel))}}
	useFakeGitHub(t, f)
	t.Setenv("APIGATE_OWNERS", "reliant-01")
	if _, _, approved, err := prDeclaration("5"); err != nil || approved {
		t.Fatalf("APIGATE_OWNERS=reliant-01 must not count the bot's label: approved=%v err=%v", approved, err)
	}
}

// A GitHub error never reads as approval: the gate fails rather than guess.
func TestPRDeclarationAPIErrorsFail(t *testing.T) {
	cases := []struct {
		name string
		f    *fakeGitHub
		want string
	}{
		{"reading the PR fails", &fakeGitHub{prErr: errors.New("HTTP 502")}, "GitHub API error (HTTP 502) reading PR 5 from GitHub"},
		{"the PR is not JSON", &fakeGitHub{pr: "<html>"}, "unreadable response reading PR 5 from GitHub"},
		{"reading the events fails", &fakeGitHub{pr: prApproved, eventsErr: errors.New("HTTP 403")}, "reading PR 5's label events"},
		{"an events page is not JSON", &fakeGitHub{pr: prApproved,
			events: []string{page(labeled("reliant-01", ApprovedLabel)), "{oops"}}, "unreadable response reading PR 5's label events"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useFakeGitHub(t, tc.f)
			_, _, approved, err := prDeclaration("5")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
			var gh *githubReadError
			if !errors.As(err, &gh) || !strings.Contains(err.Error(), "infrastructure:") || !strings.Contains(err.Error(), "not an API break") {
				t.Errorf("err = %v: a failed read must be reported as infrastructure, not as a break", err)
			}
			if approved {
				t.Error("an error must never report approval")
			}
		})
	}
}

// forge#2459: an anonymous read hit the runner's shared rate limit, curl said
// only "returned error: 403", and the required check read like a verdict. Every
// way a read can fail names its cause.
func TestClassifyGitHubFailure(t *testing.T) {
	cases := []struct {
		name     string
		exitCode int
		stderr   string
		want     string
	}{
		{"forge#2459: curl --fail on an anonymous 403", 22,
			"curl: (22) The requested URL returned error: 403", "GitHub API refused the read (HTTP 403, most often a rate limit)"},
		{"the adapter's exhausted rate limit", 75,
			"ci-public-gh: infrastructure: GitHub API rate limit (HTTP 403) reading repos/reliant-labs/forge/pulls/7 after 1 attempt(s)",
			"GitHub API rate limit (HTTP 403)"},
		{"gh's primary rate limit", 1,
			"gh: API rate limit exceeded for installation ID 1. (HTTP 403)", "GitHub API rate limit (HTTP 403)"},
		{"a secondary rate limit", 1, "HTTP 429: Too Many Requests", "GitHub API rate limit (HTTP 429)"},
		{"a rate limit with no status", 1, "You have exceeded a secondary rate limit", "GitHub API rate limit"},
		{"a 5xx", 75, "ci-public-gh: infrastructure: GitHub API error (HTTP 502) reading x", "GitHub API error (HTTP 502)"},
		{"curl cannot connect", 75,
			"ci-public-gh: infrastructure: GitHub API unreachable (curl: (7) Failed to connect to api.github.com port 443)", "GitHub API unreachable"},
		{"gh cannot resolve the host", 1,
			"Get \"https://api.github.com/repos/o/r/pulls/5\": dial tcp: lookup api.github.com: no such host", "GitHub API unreachable"},
		{"a 404", 1, "ci-public-gh: GitHub API returned HTTP 404 reading x: Not Found", "GitHub API returned HTTP 404"},
		{"tempfail with nothing said", 75, "", "GitHub API temporarily unavailable"},
		{"anything else", 1, "boom", "GitHub API read failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyGitHubFailure(tc.exitCode, tc.stderr); got != tc.want {
				t.Errorf("classifyGitHubFailure(%d, %q) = %q, want %q", tc.exitCode, tc.stderr, got, tc.want)
			}
		})
	}
}

// A read GitHub refused exits exitInfrastructure with a message that says it is
// not an API break; a verdict keeps exit 1.
func TestReportSeparatesInfrastructureFromVerdicts(t *testing.T) {
	useFakeGitHub(t, &fakeGitHub{prErr: &ghFailure{bin: "./scripts/ci-public-gh.sh", started: true, exitCode: 75, err: errors.New("exit status 75"),
		stderr: "ci-public-gh: infrastructure: GitHub API rate limit (HTTP 403) reading repos/o/r/pulls/5 after 1 attempt(s)"}})
	_, _, _, err := prDeclaration("5")
	var stdout, stderr strings.Builder
	if code := report(err, &stdout, &stderr, true); code != exitInfrastructure {
		t.Fatalf("exit %d, want %d (infrastructure)\n%s", code, exitInfrastructure, stderr.String())
	}
	for _, want := range []string{
		"apigate: infrastructure: GitHub API rate limit (HTTP 403) reading PR 5 from GitHub; this is not an API break",
		"nothing in the diff was judged incompatible",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
		}
	}
	if !strings.HasPrefix(stdout.String(), "::error title=") || strings.Count(stdout.String(), "\n") != 1 {
		t.Errorf("want one GitHub Actions error annotation on stdout, got %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := report(errStaleMarker, &stdout, &stderr, true); code != 1 || stdout.Len() != 0 {
		t.Errorf("a verdict must exit 1 with no infrastructure annotation: exit %d, stdout %q", code, stdout.String())
	}
	if code := report(nil, &stdout, &stderr, true); code != 0 {
		t.Errorf("no error must exit 0, got %d", code)
	}
}

// End to end through -pr: the same declared break that ships its codemod
// passes when the owner (or the Reliant bot) applied breaking:approved, and
// fails when anyone else did.
func TestRemovalDeclaredWithCodemodViaPR(t *testing.T) {
	slow(t)
	repo := prRepo(t, "removal")
	if err := os.MkdirAll(filepath.Join(repo, "internal", "codemod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "codemod", "widgetclose.go"), []byte(fixtureCodemod), 0o644); err != nil {
		t.Fatal(err)
	}

	useFakeGitHub(t, &fakeGitHub{pr: prApproved, events: []string{page(labeled("reliant-01", ApprovedLabel))}})
	if stdout, stderr, err := gate(t, repo, "-pr", "5"); err != nil || !strings.Contains(stdout, "declared and shipped") {
		t.Fatalf("an owner-approved declared break with its codemod must pass: %v\n%s%s", err, stdout, stderr)
	}

	useFakeGitHub(t, &fakeGitHub{pr: prApproved, events: []string{page(labeled("reliant-labs[bot]", ApprovedLabel))}})
	if stdout, stderr, err := gate(t, repo, "-pr", "5"); err != nil || !strings.Contains(stdout, "declared and shipped") {
		t.Fatalf("the same break with the Reliant bot's label must pass: %v\n%s%s", err, stdout, stderr)
	}

	useFakeGitHub(t, &fakeGitHub{pr: prApproved, events: []string{page(labeled("someone", ApprovedLabel))}})
	if _, stderr, err := gate(t, repo, "-pr", "5"); err == nil || !strings.Contains(stderr, "breaking:approved") {
		t.Fatalf("the same break with another account's label must fail naming breaking:approved: %v\n%s", err, stderr)
	}
}
