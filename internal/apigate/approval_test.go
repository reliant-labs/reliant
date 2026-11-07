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
// default owner, reliant-01.
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
// while it is on the PR and the last account to apply it is the owner's.
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
		{"a bot applied the label", prApproved,
			[]string{page(labeled("reliant-labs[bot]", ApprovedLabel))}, false},
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
			[]string{page(labeled("reliant-01", ApprovedLabel)), page(other), page(labeled("reliant-labs[bot]", ApprovedLabel))}, false},
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
}

// A GitHub error never reads as approval: the gate fails rather than guess.
func TestPRDeclarationAPIErrorsFail(t *testing.T) {
	cases := []struct {
		name string
		f    *fakeGitHub
		want string
	}{
		{"reading the PR fails", &fakeGitHub{prErr: errors.New("HTTP 502")}, "read PR 5 from GitHub"},
		{"the PR is not JSON", &fakeGitHub{pr: "<html>"}, "decode PR 5"},
		{"reading the events fails", &fakeGitHub{pr: prApproved, eventsErr: errors.New("HTTP 403")}, "label events"},
		{"an events page is not JSON", &fakeGitHub{pr: prApproved,
			events: []string{page(labeled("reliant-01", ApprovedLabel)), "{oops"}}, "decode PR 5's label events"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useFakeGitHub(t, tc.f)
			_, _, approved, err := prDeclaration("5")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
			if approved {
				t.Error("an error must never report approval")
			}
		})
	}
}

// End to end through -pr: the same declared break that ships its codemod
// passes when the owner applied breaking:approved, and fails otherwise.
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
	if _, stderr, err := gate(t, repo, "-pr", "5"); err == nil || !strings.Contains(stderr, "breaking:approved") {
		t.Fatalf("the same break with a bot-applied label must fail naming breaking:approved: %v\n%s", err, stderr)
	}
}
