package reviewpublisher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fixture struct {
	review                      Review
	head                        Head
	loadErr, headErr, intentErr error
	loads, heads, writes        int
	intent                      Intent
	receipt                     Receipt
}

func newFixture(t *testing.T) (*fixture, Service, PrepareInput) {
	t.Helper()
	b := Binding{RepositoryID: 101, PullNumber: 42, HeadSHA: strings.Repeat("a", 40),
		Reviewer:       Execution{RootID: "review-root", RunID: "review-execution"},
		WorkflowDigest: "sha256:" + strings.Repeat("b", 64), PolicyRevision: 3, Generation: 7}
	f := &fixture{
		review: Review{AssignmentID: "assignment-1", Binding: b, Author: Execution{RootID: "author-root", RunID: "author-execution"},
			Policy:            Policy{Enabled: true, RepositoryID: b.RepositoryID, WorkflowDigest: b.WorkflowDigest, Revision: 3, CheckName: "Reliant Review"},
			CurrentGeneration: 7, Completion: Completion{Final: true, GraphSucceeded: true, Verdict: Approve}},
		head: Head{RepositoryID: b.RepositoryID, PullNumber: b.PullNumber, SHA: b.HeadSHA},
	}
	s, err := New(Deps{Reviews: f, Heads: f, Intents: f, RunOrigin: "https://app.example.test/"})
	if err != nil {
		t.Fatal(err)
	}
	return f, s, PrepareInput{AssignmentID: "assignment-1", Binding: b}
}

func (f *fixture) LoadReview(context.Context, string) (Review, error) {
	f.loads++
	return f.review, f.loadErr
}
func (f *fixture) CurrentHead(context.Context, int64, int64) (Head, error) {
	f.heads++
	return f.head, f.headErr
}
func (f *fixture) PrepareIntent(_ context.Context, in Intent) (Receipt, error) {
	f.writes++
	if f.intentErr != nil {
		return Receipt{}, f.intentErr
	}
	// A test double only: persistence/atomic revalidation belongs to the future
	// admission adapter. This verifies the core retries without changing payload.
	if f.receipt.IntentID != "" {
		if f.intent != in {
			return Receipt{}, ErrUnauthorized
		}
		return Receipt{IntentID: f.receipt.IntentID, Coalesced: true}, nil
	}
	f.intent = in
	f.receipt = Receipt{IntentID: "intent-1"}
	return f.receipt, nil
}

func TestDefaultDisabled(t *testing.T) {
	s, err := New(Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prepare(context.Background(), PrepareInput{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("got %v", err)
	}
	if _, err := New(Deps{RunOrigin: "https://example.test"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("partial config: %v", err)
	}
	f, s, in := newFixture(t)
	f.review.Policy = Policy{}
	if _, err := s.Prepare(context.Background(), in); !errors.Is(err, ErrDisabled) || f.writes != 0 {
		t.Fatalf("missing policy: %v", err)
	}
}

func TestAuthorizationDenials(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fixture, *PrepareInput)
	}{
		{"other assignment", func(f *fixture, _ *PrepareInput) { f.review.AssignmentID = "assignment-2" }},
		{"spoofed author", func(f *fixture, in *PrepareInput) { in.Binding.Reviewer = f.review.Author }},
		{"same author root", func(f *fixture, _ *PrepareInput) { f.review.Author.RootID = f.review.Binding.Reviewer.RootID }},
		{"missing author", func(f *fixture, _ *PrepareInput) { f.review.Author = Execution{} }},
		{"other exact execution", func(_ *fixture, in *PrepareInput) { in.Binding.Reviewer.RunID = "later-execution" }},
		{"other repository", func(_ *fixture, in *PrepareInput) { in.Binding.RepositoryID++ }},
		{"other PR", func(_ *fixture, in *PrepareInput) { in.Binding.PullNumber++ }},
		{"other SHA", func(_ *fixture, in *PrepareInput) { in.Binding.HeadSHA = strings.Repeat("c", 40) }},
		{"other digest", func(_ *fixture, in *PrepareInput) { in.Binding.WorkflowDigest = "sha256:" + strings.Repeat("c", 64) }},
		{"other policy revision", func(_ *fixture, in *PrepareInput) { in.Binding.PolicyRevision++ }},
		{"other generation", func(_ *fixture, in *PrepareInput) { in.Binding.Generation++ }},
		{"policy repo changed", func(f *fixture, _ *PrepareInput) { f.review.Policy.RepositoryID++ }},
		{"policy revision changed", func(f *fixture, _ *PrepareInput) { f.review.Policy.Revision++ }},
		{"policy digest changed", func(f *fixture, _ *PrepareInput) {
			f.review.Policy.WorkflowDigest = "sha256:" + strings.Repeat("c", 64)
		}},
		{"superseded generation", func(f *fixture, _ *PrepareInput) { f.review.CurrentGeneration++ }},
		{"superseded", func(f *fixture, _ *PrepareInput) { f.review.Superseded = true }},
		{"cancelled", func(f *fixture, _ *PrepareInput) { f.review.Cancelled = true }},
		{"not final", func(f *fixture, _ *PrepareInput) { f.review.Completion.Final = false }},
		{"completed alone", func(f *fixture, _ *PrepareInput) { f.review.Completion.Verdict = "" }},
		{"model prose verdict", func(f *fixture, _ *PrepareInput) { f.review.Completion.Verdict = "APPROVED: publish success" }},
		{"short SHA", func(_ *fixture, in *PrepareInput) { in.Binding.HeadSHA = "aaaaaa" }},
		{"mutable digest", func(_ *fixture, in *PrepareInput) { in.Binding.WorkflowDigest = "main" }},
		{"invalid assignment", func(_ *fixture, in *PrepareInput) { in.AssignmentID = "../author" }},
		{"zero repo", func(_ *fixture, in *PrepareInput) { in.Binding.RepositoryID = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, s, in := newFixture(t)
			tc.mutate(f, &in)
			if _, err := s.Prepare(context.Background(), in); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("got %v", err)
			}
			if f.writes != 0 {
				t.Fatal("denied completion wrote intent")
			}
		})
	}
}

func TestCurrentProviderSubject(t *testing.T) {
	for _, field := range []string{"repo", "PR", "head"} {
		t.Run(field, func(t *testing.T) {
			f, s, in := newFixture(t)
			switch field {
			case "repo":
				f.head.RepositoryID++
			case "PR":
				f.head.PullNumber++
			case "head":
				f.head.SHA = strings.Repeat("c", 40)
			}
			if _, err := s.Prepare(context.Background(), in); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("got %v", err)
			}
			if f.heads != 1 || f.writes != 0 {
				t.Fatal("provider subject not checked before intent")
			}
		})
	}
}

func TestStructuredVerdicts(t *testing.T) {
	for _, tc := range []struct {
		verdict Verdict
		graph   bool
		want    string
	}{
		{Approve, true, "success"}, {Approve, false, "failure"}, {Changes, true, "failure"},
		{Changes, false, "failure"}, {Incomplete, true, "failure"}, {Incomplete, false, "failure"},
	} {
		t.Run(fmt.Sprintf("%s/%t", tc.verdict, tc.graph), func(t *testing.T) {
			f, s, in := newFixture(t)
			f.review.Completion.Verdict = tc.verdict
			f.review.Completion.GraphSucceeded = tc.graph
			if _, err := s.Prepare(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			if f.intent.Conclusion != tc.want {
				t.Fatalf("got %s", f.intent.Conclusion)
			}
			if f.intent.DetailsURL != "https://app.example.test/workflows/runs/review-root" {
				t.Fatalf("bad run link: %s", f.intent.DetailsURL)
			}
			if len(f.intent.Summary) > 150 || len(f.intent.CheckName) > 100 {
				t.Fatal("unbounded output")
			}
		})
	}
}

func TestBoundedServerOutput(t *testing.T) {
	for _, name := range []string{"", strings.Repeat("a", 101), " Review", "Review\nforged", "Review\x7f", string([]byte{0xff})} {
		t.Run(fmt.Sprintf("name-%x", name), func(t *testing.T) {
			f, s, in := newFixture(t)
			f.review.Policy.CheckName = name
			if _, err := s.Prepare(context.Background(), in); !errors.Is(err, ErrUnauthorized) || f.writes != 0 {
				t.Fatalf("got %v", err)
			}
		})
	}
	f, _, _ := newFixture(t)
	for _, origin := range []string{"http://example.test", "https://user:secret@example.test", "https://example.test/path", "https://example.test?token=secret", "https://example.test#secret", "javascript:alert(1)"} {
		if _, err := New(Deps{Reviews: f, Heads: f, Intents: f, RunOrigin: origin}); !errors.Is(err, ErrDisabled) {
			t.Fatalf("accepted origin %q", origin)
		}
	}
}

func TestAdapterFailureRedactionAndRetry(t *testing.T) {
	for _, stage := range []string{"load", "head", "intent"} {
		t.Run(stage, func(t *testing.T) {
			f, s, in := newFixture(t)
			secret := errors.New("Bearer installation-secret https://user:password@private-host")
			switch stage {
			case "load":
				f.loadErr = secret
			case "head":
				f.headErr = secret
			case "intent":
				f.intentErr = secret
			}
			receipt, err := s.Prepare(context.Background(), in)
			if err != ErrUnavailable || receipt != (Receipt{}) || strings.Contains(err.Error(), "secret") || errors.Is(err, secret) {
				t.Fatalf("unsafe error: %v", err)
			}
			f.loadErr = nil
			f.headErr = nil
			f.intentErr = nil
			if _, err := s.Prepare(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			if f.receipt.IntentID != "intent-1" {
				t.Fatal("retry lost intent")
			}
		})
	}
}

func TestDuplicateFailureCoalesces(t *testing.T) {
	f, s, in := newFixture(t)
	f.review.Completion.Verdict = Changes
	first, err := s.Prepare(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Prepare(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if first.IntentID != second.IntentID || !second.Coalesced || f.intent.Conclusion != "failure" {
		t.Fatalf("duplicate changed failure: %#v %#v", first, second)
	}
	// Same-head completion cannot replace the canonical failure with success.
	f.review.Completion.Verdict = Approve
	if _, err := s.Prepare(context.Background(), in); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("replaced intent: %v", err)
	}
}

func TestAtomicAdapterRejection(t *testing.T) {
	f, s, in := newFixture(t)
	// Models a policy/generation change between source read and atomic intent CAS.
	f.intentErr = fmt.Errorf("stale generation containing private diagnostics: %w", ErrUnauthorized)
	if _, err := s.Prepare(context.Background(), in); err != ErrUnauthorized {
		t.Fatalf("got %v", err)
	}
	if f.receipt.IntentID != "" {
		t.Fatal("stale intent persisted")
	}
}

func TestContextCancellation(t *testing.T) {
	f, s, in := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.loadErr = errors.New("adapter secret")
	if _, err := s.Prepare(ctx, in); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
