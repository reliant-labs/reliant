// Copyright (c) 2026 Reliant Labs
package recorddue_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/recorddue"
)

// starter records the event-fire workflows the intake starts.
type starter struct {
	mu  sync.Mutex
	ids []string
}

func (s *starter) ExecuteWorkflow(_ context.Context, o client.StartWorkflowOptions, _ any, _ ...any) (client.WorkflowRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, o.ID)
	return nil, nil
}

type harness struct {
	t       *testing.T
	env     *storetest.Env
	repo    *core.PipelineRepo
	starter *starter
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	env := storetest.New(t)
	ctx := context.Background()
	repo, err := env.Repo.UpsertPipelineRepo(ctx, core.PipelineRepoEnrolment{
		OrgID: "org-1", ProjectID: storetest.SeedProjectID, Provider: "github", AccountKey: "inst-1",
		RepositoryID: "42", Repository: "acme/app", CreatedByUserID: storetest.SeedUserID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo, err = env.Repo.SetPipelineRepoMode(ctx, repo.ID, repo.Version, core.PipelineRepoModeShadow); err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, env: env, repo: repo, starter: &starter{}}
}

func (h *harness) exec(q string, args ...any) {
	h.t.Helper()
	if _, err := h.env.SQL.Exec(q, args...); err != nil {
		h.t.Fatalf("%s: %v", q, err)
	}
}

// pr records PR number at head and gives it a pr_pipeline row due at due
// (nil: waiting for nothing).
func (h *harness) pr(id string, number int, due *time.Time) {
	h.t.Helper()
	h.exec(`INSERT INTO pr_records (id, provider, account_key, repository_id, repository, number, state,
		head_sha, snapshot_at, observed_at) VALUES ($1, 'github', 'inst-1', '42', 'acme/app', $2, 'open', $3, now(), now())`,
		id, number, "head-"+id)
	p := core.PRPipelineNew{RecordID: id, OrgID: h.repo.OrgID, ProjectID: h.repo.ProjectID, RepoID: h.repo.ID, StateHeadSHA: "head-" + id}
	if due != nil {
		p.NextAction, p.NextActionAt = "decide", due
	}
	if _, _, err := h.env.Repo.EnsurePRPipeline(context.Background(), p); err != nil {
		h.t.Fatal(err)
	}
}

// activation inserts an enabled record_due trigger of the seed project in org.
func (h *harness) activation(id, org string) {
	h.t.Helper()
	h.exec(`INSERT INTO triggers (id, user_id, project_id, org_id, name, kind, workflow, enabled, no_machine, config)
		VALUES ($1, $2, $3, $4, $1, 'record_due', 'pr', true, true, '{"table": "pr_pipeline"}'::jsonb)`,
		id, storetest.SeedUserID, storetest.SeedProjectID, org)
}

func (h *harness) scanner() recorddue.Service {
	intake := triggers.NewIntake(triggers.NewStores(h.env.Repo), h.starter, "test-queue")
	return recorddue.New(recorddue.Deps{Store: h.env.Repo, Intake: intake})
}

func (h *harness) events() []map[string]any {
	h.t.Helper()
	rows, err := h.env.SQL.Query(`SELECT trigger_id, dedupe_key, outcome, payload::text FROM trigger_events
		WHERE kind = 'record_due' ORDER BY dedupe_key`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var trigger, key, outcome, payload string
		if err := rows.Scan(&trigger, &key, &outcome, &payload); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, map[string]any{"trigger": trigger, "key": key, "outcome": outcome, "payload": payload})
	}
	return out
}

func TestScanFiresEachDueRecordOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	past := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	future := time.Now().UTC().Add(time.Hour)
	h.pr("rec-due", 7, &past)
	h.pr("rec-later", 8, &future)
	h.pr("rec-idle", 9, nil)
	h.activation("trg-pr", "org-1")
	h.activation("trg-other-org", "org-2")

	fired, err := h.scanner().Scan(context.Background())
	if err != nil || fired != 1 {
		t.Fatalf("first scan: fired %d, %v", fired, err)
	}
	evs := h.events()
	if len(evs) != 1 || evs[0]["trigger"] != "trg-pr" || evs[0]["outcome"] != string(core.TriggerEventPending) ||
		evs[0]["key"] != recorddue.DedupeKey("trg-pr", "rec-due", past) {
		t.Fatalf("events: %+v", evs)
	}
	for _, want := range []string{`"record_id": "rec-due"`, `"repository": "acme/app"`, `"number": 7`, `"head_sha": "head-rec-due"`,
		`"next_action": "decide"`, `"table": "pr_pipeline"`} {
		if !strings.Contains(evs[0]["payload"].(string), want) {
			t.Fatalf("payload %s lacks %s", evs[0]["payload"], want)
		}
	}
	if len(h.starter.ids) != 1 {
		t.Fatalf("fires started: %v", h.starter.ids)
	}

	// The record stays due until it is acted on; a later scan fires nothing.
	if fired, err := h.scanner().Scan(context.Background()); err != nil || fired != 0 {
		t.Fatalf("second scan: fired %d, %v", fired, err)
	}
	if len(h.events()) != 1 || len(h.starter.ids) != 1 {
		t.Fatalf("a repeat scan fired again: %+v", h.events())
	}

	// Acted on and due again (a new due time): it fires again.
	row, err := h.env.Repo.GetPRPipeline(context.Background(), "rec-due")
	if err != nil {
		t.Fatal(err)
	}
	again := past.Add(30 * time.Second)
	to := *row
	to.NextActionAt = &again
	if _, err := h.env.Repo.TransitionPRPipeline(context.Background(), row.RecordID, row.Version, to); err != nil {
		t.Fatal(err)
	}
	if fired, err := h.scanner().Scan(context.Background()); err != nil || fired != 1 {
		t.Fatalf("new due time: fired %d, %v", fired, err)
	}
}

func TestScanAdvancesPastStillDueBatch(t *testing.T) {
	for _, unmatched := range []bool{false, true} {
		t.Run(fmt.Sprintf("unmatched=%t", unmatched), func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			ctx := context.Background()
			past := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
			// Equal due times exercise the record_id tie breaker as well.
			for i := 0; i <= recorddue.BatchSize; i++ {
				id := fmt.Sprintf("rec-%03d", i)
				h.pr(id, i+1, &past)
				if unmatched && i < recorddue.BatchSize {
					h.exec(`UPDATE pr_pipeline SET org_id = 'org-other' WHERE record_id = $1`, id)
				}
			}
			h.activation("trg-pr", "org-1")
			s := h.scanner()
			wantFirst := recorddue.BatchSize
			if unmatched {
				wantFirst = 0
			}
			if n, err := s.Scan(ctx); err != nil || n != wantFirst {
				t.Fatalf("first page: fired %d, %v; want %d", n, err, wantFirst)
			}
			if n, err := s.Scan(ctx); err != nil || n != 1 {
				t.Fatalf("later row starved: fired %d, %v", n, err)
			}
			evs := h.events()
			wantKey := recorddue.DedupeKey("trg-pr", fmt.Sprintf("rec-%03d", recorddue.BatchSize), past)
			if len(evs) != wantFirst+1 || evs[len(evs)-1]["key"] != wantKey {
				t.Fatalf("later row event missing: %+v", evs)
			}
			var stillDue int
			if err := h.env.SQL.QueryRow(`SELECT count(*) FROM pr_pipeline WHERE next_action_at = $1`, past).Scan(&stillDue); err != nil {
				t.Fatal(err)
			}
			if stillDue != recorddue.BatchSize+1 {
				t.Fatalf("scan changed due state: %d rows remain due", stillDue)
			}
			// Wrapping revisits the first batch without recording duplicates.
			if n, err := s.Scan(ctx); err != nil || n != 0 {
				t.Fatalf("wrapped scan: fired %d, %v", n, err)
			}
			if len(h.events()) != wantFirst+1 {
				t.Fatal("wrapped scan recorded duplicate events")
			}
		})
	}
}

func TestScanDedupesAcrossScanners(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	past := time.Now().UTC().Add(-time.Minute)
	for i, id := range []string{"rec-a", "rec-b", "rec-c"} {
		h.pr(id, 10+i, &past)
	}
	h.activation("trg-pr", "org-1")

	a, b := h.scanner(), h.scanner()
	var wg sync.WaitGroup
	totals := make([]int, 4)
	for i := range totals {
		s := a
		if i%2 == 1 {
			s = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := s.Scan(context.Background())
			if err != nil {
				t.Error(err)
			}
			totals[i] = n
		}()
	}
	wg.Wait()
	sum := 0
	for _, n := range totals {
		sum += n
	}
	if evs := h.events(); sum != 3 || len(evs) != 3 {
		t.Fatalf("fired %d (%v), %d events: each due record fires exactly once", sum, totals, len(evs))
	}
}

func TestScanFiresNothingForARepositoryWithModeOff(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	past := time.Now().UTC().Add(-time.Minute)
	h.pr("rec-due", 7, &past)
	h.activation("trg-pr", "org-1")
	if _, err := h.env.Repo.SetPipelineRepoMode(context.Background(), h.repo.ID, h.repo.Version, core.PipelineRepoModeOff); err != nil {
		t.Fatal(err)
	}
	if fired, err := h.scanner().Scan(context.Background()); err != nil || fired != 0 || len(h.events()) != 0 {
		t.Fatalf("mode off fired %d, %v", fired, err)
	}
}

func TestScanWithNoActivationFiresNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	past := time.Now().UTC().Add(-time.Minute)
	h.pr("rec-due", 7, &past)
	if fired, err := h.scanner().Scan(context.Background()); err != nil || fired != 0 || len(h.events()) != 0 {
		t.Fatalf("no activation fired %d, %v", fired, err)
	}
}
