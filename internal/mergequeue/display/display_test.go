// Copyright (c) 2025 Reliant Labs
package display_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/display"
	"github.com/reliant-labs/reliant/internal/mergequeue/fakegithub"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
)

func TestFromStateRendersEveryQueueState(t *testing.T) {
	batch := &core.QueueBatch{ID: "b1", State: core.QueueBatchGating}
	for _, tc := range []struct {
		name       string
		entry      *core.QueueEntry
		pp         *core.PRPipeline
		state      display.State
		status     ghport.CheckStatus
		conclusion ghport.CheckConclusion
		comment    bool
	}{
		{name: "queued", entry: &core.QueueEntry{ID: "e", HeadSHA: "h", State: core.QueueEntryWaiting},
			state: display.StateQueued, status: ghport.CheckQueued},
		{name: "gating", entry: &core.QueueEntry{ID: "e", HeadSHA: "h", State: core.QueueEntryBatched, BatchID: "b1"},
			state: display.StateGating, status: ghport.CheckInProgress},
		{name: "landed", entry: &core.QueueEntry{ID: "e", HeadSHA: "h", State: core.QueueEntryLanded, LandedSHA: "m1"},
			state: display.StateLanded, status: ghport.CheckCompleted, conclusion: ghport.ConclusionSuccess},
		{name: "ejected", entry: &core.QueueEntry{ID: "e", HeadSHA: "h", State: core.QueueEntryEjected, EjectReason: core.QueueEjectGateRed},
			state: display.StateEjected, status: ghport.CheckCompleted, conclusion: ghport.ConclusionFailure, comment: true},
		{name: "needs person", entry: &core.QueueEntry{ID: "e", HeadSHA: "h", State: core.QueueEntryEjected, EjectReason: core.QueueEjectGateRed},
			pp:    &core.PRPipeline{State: core.PRPipelineNeedsPerson, NeedsPersonReason: "fix rounds exhausted"},
			state: display.StateNeedsPerson, status: ghport.CheckCompleted, conclusion: ghport.ConclusionActionRequired, comment: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := display.FromState(7, tc.entry, batch, tc.pp)
			if v.State != tc.state || v.Number != 7 || v.Head != "h" {
				t.Fatalf("view = %+v", v)
			}
			c := display.Check(v)
			if c.Name != "Reliant" || c.Status != tc.status || c.Conclusion != tc.conclusion || c.HeadSHA != "h" {
				t.Fatalf("check = %+v", c)
			}
			if err := ghport.ValidateCheckRun(c.Status, c.Conclusion); err != nil {
				t.Fatal(err)
			}
			body, ok := display.Comment(v)
			if ok != tc.comment {
				t.Fatalf("comment = %v %q", ok, body)
			}
			if ok && !strings.Contains(body, "<!-- reliant-queue:") {
				t.Fatalf("comment has no marker: %q", body)
			}
		})
	}
}

// memStore keeps one batch's verdict, merging patches like the store does.
type memStore struct{ b *core.QueueBatch }

func (m *memStore) GetQueueBatch(context.Context, string) (*core.QueueBatch, error) { return m.b, nil }

func (m *memStore) PatchQueueBatchVerdict(_ context.Context, _ string, patch json.RawMessage) (*core.QueueBatch, error) {
	doc := map[string]json.RawMessage{}
	_ = json.Unmarshal(m.b.VerdictJSON, &doc)
	var p map[string]json.RawMessage
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, err
	}
	for k, v := range p {
		doc[k] = v
	}
	m.b.VerdictJSON, _ = json.Marshal(doc)
	return m.b, nil
}

func TestShadowRecordsInsteadOfWritingGitHub(t *testing.T) {
	g := fakegithub.New(t)
	r := g.NewRepo("acme/widgets")
	head := r.Commit("main", "base", map[string]string{"a": "a\n"})
	st := &memStore{b: &core.QueueBatch{ID: "b1", VerdictJSON: json.RawMessage(`{"mode":"shadow"}`)}}
	p := &display.Projector{Store: st}
	repo := &core.PipelineRepo{Repository: "acme/widgets", Mode: core.PipelineRepoModeShadow}

	if err := p.Publish(t.Context(), repo, g, "b1", []display.View{{Number: 1, Head: head, State: display.StateGating, BatchID: "b1"}}); err != nil {
		t.Fatal(err)
	}
	if err := p.Publish(t.Context(), repo, g, "b1", []display.View{{Number: 2, Head: head, State: display.StateEjected, Reason: "conflict", EntryID: "e2"}}); err != nil {
		t.Fatal(err)
	}
	if len(g.Calls()) != 0 {
		t.Fatalf("shadow wrote to GitHub: %+v", g.Calls())
	}
	got := display.Recorded(st.b.VerdictJSON)
	if len(got) != 2 || got[0].Check.Status != ghport.CheckInProgress || got[1].Comment == "" {
		t.Fatalf("recorded = %+v", got)
	}
	if !strings.Contains(string(st.b.VerdictJSON), `"mode":"shadow"`) {
		t.Fatal("the scheduler's verdict keys must survive")
	}

	off := &core.PipelineRepo{Repository: "acme/widgets", Mode: core.PipelineRepoModeOff}
	if err := p.Publish(t.Context(), off, g, "b1", []display.View{{Number: 1, Head: head, State: display.StateQueued}}); err != nil || len(g.Calls()) != 0 {
		t.Fatalf("mode off wrote: %v %+v", err, g.Calls())
	}
}

func TestOnUpdatesOneCheckAndCommentsOnce(t *testing.T) {
	g := fakegithub.New(t)
	r := g.NewRepo("acme/widgets")
	r.Commit("main", "base", map[string]string{"a": "a\n"})
	r.Branch("feat", "main")
	head := r.Commit("feat", "work", map[string]string{"b": "b\n"})
	n := r.OpenPR(fakegithub.PROptions{Head: "feat"})
	p := &display.Projector{}
	repo := &core.PipelineRepo{Repository: "acme/widgets", Mode: core.PipelineRepoModeOn}
	publish := func(v display.View) {
		t.Helper()
		v.Number, v.Head = n, head
		if err := p.Publish(t.Context(), repo, g, "b1", []display.View{v}); err != nil {
			t.Fatal(err)
		}
	}
	publish(display.View{State: display.StateGating})
	publish(display.View{State: display.StateEjected, Reason: "gate_red", EntryID: "e1"})
	publish(display.View{State: display.StateEjected, Reason: "gate_red", EntryID: "e1"})

	runs, err := g.ListCheckRuns(t.Context(), r.ID(), head, ghport.ListCheckRuns{Name: "Reliant"})
	if err != nil || len(runs) != 1 || runs[0].Conclusion != ghport.ConclusionFailure {
		t.Fatalf("runs = %+v (%v)", runs, err)
	}
	if c := r.Comments(n); len(c) != 1 || !strings.Contains(c[0].Body, "the gate failed") {
		t.Fatalf("comments = %+v", c)
	}
	// Queued again after a completed run: a new run, since GitHub cannot
	// reopen one.
	publish(display.View{State: display.StateQueued})
	if runs, _ = g.ListCheckRuns(t.Context(), r.ID(), head, ghport.ListCheckRuns{Name: "Reliant"}); len(runs) != 2 || runs[0].Status != ghport.CheckQueued {
		t.Fatalf("runs = %+v", runs)
	}
}
