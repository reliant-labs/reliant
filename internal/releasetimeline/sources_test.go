package releasetimeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

// ── local ledger ────────────────────────────────────────────────────────────

func digest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

func ociRelease(version string, created time.Time, commit string, rng *release.CommitRange) release.Release {
	return release.Release{
		Version: version, CreatedAt: created, Git: release.Git{Commit: commit}, Commits: rng,
		Artifacts: map[string]release.Artifact{"ghcr.io/acme/api": {
			Kind: release.KindOCI, Mode: release.ModeShared,
			Digests: map[string]string{release.SharedVariant: digest('b')},
		}},
	}
}

// TestLocal_ReadsAMachineLedgerFixture drives the real file ledger under a
// temp home and checks the timeline built from it end to end.
func TestLocal_ReadsAMachineLedgerFixture(t *testing.T) {
	store, err := ledgerfile.Open(t.TempDir(), "proj-abc123")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for _, r := range []release.Release{
		ociRelease("v1", base, sha('a'), nil),
		ociRelease("v2", base.Add(time.Minute), sha('b'), &release.CommitRange{
			BaseRelease: "v1", BaseCommit: sha('a'), Total: 1,
			Commits: []release.Commit{{SHA: sha('b'), Subject: "add x (#9)"}},
		}),
	} {
		if _, err := store.CutRelease(r); err != nil {
			t.Fatal(err)
		}
	}
	promote := func(version string) release.Promotion {
		p, err := store.AppendPromotion(release.Promotion{
			Env: "prod", Release: version, Kind: release.KindPromote,
			Resolved:   map[string]string{"api": digest('c')},
			PromotedBy: release.Actor{Actor: "ci"},
			Run:        release.Run{ID: "gh/1", Provider: "github", URL: "https://ci/1"},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p1, p2, p3 := promote("v1"), promote("v2"), promote("v1") // a rollback to v1

	if err := store.AppendGates("prod", p2.ID, []release.Gate{{Name: "smoke", Status: release.GateStatusFailed, Summary: "502"}}); err != nil {
		t.Fatal(err)
	}
	// p2's apply failed; p3's succeeded.
	for _, c := range []struct {
		p      release.Promotion
		status release.ApplyStatus
		took   time.Duration
	}{{p2, release.ApplyFailed, 90 * time.Second}, {p3, release.ApplySucceeded, 30 * time.Second}} {
		a, err := store.BeginApply(release.Apply{
			Env: "prod", BundleID: "b1", PromotionID: c.p.ID,
			CreatedAt: c.p.PromotedAt, DeadlineAt: c.p.PromotedAt.Add(time.Hour),
		}, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.FinishApply("prod", release.ApplyOutcome{
			ApplyID: a.ID, Status: c.status, Summary: "rollout " + string(c.status),
			FinishedAt: c.p.PromotedAt.Add(c.took),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A running apply (no outcome) is not a result and must not become a row.
	if _, err := store.BeginApply(release.Apply{
		Env: "prod", BundleID: "b1", PromotionID: p3.ID,
		CreatedAt: p3.PromotedAt, DeadlineAt: time.Now().Add(time.Hour),
	}, true); err != nil {
		t.Fatal(err)
	}

	d, err := Local{Ledger: store}.Load(context.Background(), "prod", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	tl := Assemble(d, time.Time{}, 0)

	var failed, promotions []Entry
	for _, e := range tl.Entries {
		if e.Kind == KindFailedDeploy {
			failed = append(failed, e)
		} else {
			promotions = append(promotions, e)
		}
	}
	if len(promotions) != 3 || len(failed) != 1 {
		t.Fatalf("promotions=%d failed=%d: %+v", len(promotions), len(failed), tl.Entries)
	}
	if got := promotions[0]; got.PromotionID != p3.ID || !got.Live || got.Direction != DirBehind || got.Actor != "ci" || got.Via != "github" {
		t.Errorf("newest = %+v", got)
	}
	if promotions[1].PromotionID != p2.ID || promotions[1].Direction != DirAhead || promotions[1].GateVerdict != "failed" {
		t.Errorf("p2 = %+v", promotions[1])
	}
	if promotions[2].PromotionID != p1.ID || promotions[2].Direction != DirInitial {
		t.Errorf("p1 = %+v", promotions[2])
	}
	if promotions[0].Rollout == nil || promotions[0].Rollout.Status != StatusSucceeded || promotions[0].Rollout.DurationSeconds != 30 {
		t.Errorf("p3 rollout = %+v", promotions[0].Rollout)
	}
	if f := failed[0]; f.Version != "v2" || f.Rollout.DurationSeconds != 90 || f.Rollout.Reason != "rollout failed" {
		t.Errorf("failed row = %+v / %+v", f, f.Rollout)
	}
}

func TestLocal_AnEnvNeverPromotedIsEmptyNotAnError(t *testing.T) {
	store, err := ledgerfile.Open(t.TempDir(), "proj-abc123")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Local{Ledger: store}.Load(context.Background(), "nowhere", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if tl := Assemble(d, time.Time{}, 0); len(tl.Entries) != 0 {
		t.Errorf("entries = %+v", tl.Entries)
	}
}

type brokenLedger struct{ LedgerReader }

func (brokenLedger) Releases() ([]release.Release, error) { return nil, errors.New("disk on fire") }

func TestLocal_AnUnreadableReleaseLogCostsDirectionsNotTheTimeline(t *testing.T) {
	store, _ := ledgerfile.Open(t.TempDir(), "proj-abc123")
	if _, err := store.AppendPromotion(release.Promotion{Env: "prod", Release: "v1", Kind: release.KindPromote, Resolved: map[string]string{}}, nil); err != nil {
		t.Fatal(err)
	}
	d, err := Local{Ledger: brokenLedger{store}}.Load(context.Background(), "prod", time.Time{})
	if err != nil || len(d.Promotions) != 1 || len(d.Warnings) != 1 {
		t.Fatalf("err=%v promotions=%d warnings=%v", err, len(d.Promotions), d.Warnings)
	}
}

// ── cloud ───────────────────────────────────────────────────────────────────

type fakeCP struct {
	calls   []string
	reqs    map[string]map[string]any
	respond map[string]func(req map[string]any) (any, error)
}

func (f *fakeCP) Call(_ context.Context, proc string, req, out any) error {
	f.calls = append(f.calls, proc)
	raw, _ := json.Marshal(req)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if f.reqs == nil {
		f.reqs = map[string]map[string]any{}
	}
	f.reqs[proc] = m
	fn, ok := f.respond[proc]
	if !ok {
		return fmt.Errorf("unexpected procedure %s", proc)
	}
	resp, err := fn(m)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(resp)
	return json.Unmarshal(body, out)
}

func cloudFake() *fakeCP {
	return &fakeCP{respond: map[string]func(map[string]any) (any, error){
		procListEnvironments: func(map[string]any) (any, error) {
			return map[string]any{"environments": []map[string]any{{"id": "env-1", "name": "prod", "project": "app"}, {"id": "env-9", "name": "prod2"}}}, nil
		},
		procListPromotions: func(req map[string]any) (any, error) {
			if req["beforePromotionId"] == "p2" {
				return map[string]any{"promotions": []map[string]any{
					{"id": "p1", "releaseVersion": "v1", "createdAt": "2026-10-01T12:30:00Z", "promotedByUserId": "u-1"},
				}}, nil
			}
			return map[string]any{"nextBeforePromotionId": "p2", "promotions": []map[string]any{
				{"id": "p3", "bundleId": "b3", "releaseVersion": "v1", "createdAt": "2026-10-01T13:00:00Z", "promotedByActor": "ci",
					"run": map[string]any{"provider": "github", "url": "https://ci/3"}, "note": "rollback"},
				{"id": "p2", "releaseVersion": "v2", "createdAt": "2026-10-01T12:40:00Z", "fromEnvironmentName": "staging",
					"gates":         []map[string]any{{"name": "lint", "status": "passed"}},
					"recordedGates": []map[string]any{{"name": "smoke", "status": "failed", "summary": "502"}, {"name": "old", "status": "green"}}},
			}}, nil
		},
		procListReleases: func(req map[string]any) (any, error) {
			return map[string]any{"releases": []map[string]any{
				{"version": "v1", "gitCommit": sha('a'), "createdAt": "2026-10-01T12:00:00Z"},
				{"version": "v2", "gitCommit": sha('b'), "createdAt": "2026-10-01T12:10:00Z",
					"commits": map[string]any{"baseRelease": "v1", "baseCommit": sha('a'), "total": 1,
						"commits": []map[string]any{{"sha": sha('b'), "subject": "x (#4)", "time": "2026-10-01T12:05:00Z"}}}},
			}}, nil
		},
		procListConvergences: func(map[string]any) (any, error) {
			return map[string]any{"convergences": []map[string]any{
				{"id": "c2", "promotionId": "p2", "state": "failed", "reason": "HealthCheckFailed", "message": "pods crashing",
					"cluster": "c1", "observedAt": "2026-10-01T12:46:00Z", "firstAttemptAt": "2026-10-01T12:40:00Z"},
				{"id": "c3", "promotionId": "p3", "state": "converged", "observedAt": "2026-10-01T13:02:00Z", "healthyAt": "2026-10-01T13:01:00Z"},
			}}, nil
		},
		cloud.ProcGetOrganization: func(map[string]any) (any, error) {
			return cloud.OrganizationReply("org-1"), nil
		},
		procListActivity: func(map[string]any) (any, error) {
			return map[string]any{"events": []map[string]any{
				{"at": "2026-10-01T12:40:05Z", "verb": "promote", "actor": map[string]any{"name": "alice"}, "via": "console",
					"target": map[string]any{"id": "v2"}, "detail": map[string]any{"promotion_id": "p2"}},
			}}, nil
		},
	}}
}

func TestCloud_AssemblesTheTimelineFromTheLedgerRPCs(t *testing.T) {
	cp := cloudFake()
	d, err := NewCloud(cp, "app").Load(context.Background(), "prod", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Warnings) != 0 {
		t.Fatalf("warnings = %v", d.Warnings)
	}
	// Pages are newest first on the wire; Data is oldest first.
	var ids []string
	for _, p := range d.Promotions {
		ids = append(ids, p.ID)
	}
	if !reflect.DeepEqual(ids, []string{"p1", "p2", "p3"}) {
		t.Fatalf("promotion order = %v", ids)
	}
	if cp.reqs[procListPromotions]["environmentId"] != "env-1" || cp.reqs[procListEnvironments]["project"] != "app" {
		t.Errorf("env scoping: %+v", cp.reqs)
	}
	if got := cp.reqs[procListActivity]["verb"]; got != "promote" {
		t.Errorf("activity verb filter = %v", got)
	}

	tl := Assemble(d, time.Time{}, 0)
	byID := map[string]Entry{}
	var failed []Entry
	for _, e := range tl.Entries {
		if e.Kind == KindFailedDeploy {
			failed = append(failed, e)
		} else {
			byID[e.PromotionID] = e
		}
	}
	p3, p2 := byID["p3"], byID["p2"]
	if p3.BundleID != "b3" {
		t.Errorf("bundle id = %q", p3.BundleID)
	}
	if p3.Direction != DirBehind || p3.Actor != "ci" || p3.Via != "github" || p3.Note != "rollback" || !p3.Live {
		t.Errorf("p3 = %+v", p3)
	}
	if p3.Rollout == nil || p3.Rollout.Status != StatusSucceeded {
		t.Errorf("p3 rollout = %+v", p3.Rollout)
	}
	if p2.Actor != "alice" || p2.Via != "console" || p2.FromEnv != "staging" || p2.GateVerdict != "failed" {
		t.Errorf("p2 = %+v", p2)
	}
	if len(p2.Gates) != 3 || p2.Gates[2].Status != "error" {
		t.Errorf("an unrecognised gate status must read as error, got %+v", p2.Gates)
	}
	if len(failed) != 1 || failed[0].Version != "v2" || !strings.Contains(failed[0].Rollout.Reason, "HealthCheckFailed: pods crashing") {
		t.Errorf("failed = %+v", failed)
	}
}

func TestCloud_StopsPagingAtMaxAndSaysSo(t *testing.T) {
	defer func(old int) { maxPromotions = old }(maxPromotions)
	maxPromotions = 1
	cp := cloudFake()
	d, err := NewCloud(cp, "").Load(context.Background(), "prod", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Truncated || len(d.Promotions) != 2 {
		t.Errorf("truncated=%v promotions=%d", d.Truncated, len(d.Promotions))
	}
}

func TestCloud_StopsPagingOncePastSince(t *testing.T) {
	cp := cloudFake()
	d, err := NewCloud(cp, "").Load(context.Background(), "prod", time.Date(2026, 10, 1, 12, 50, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// Page one ends at p2 (12:40), already before since: no second page.
	if !d.Truncated || len(d.Promotions) != 2 {
		t.Errorf("truncated=%v promotions=%d", d.Truncated, len(d.Promotions))
	}
}

func TestCloud_UnknownEnvironmentIsEmptyWithAWarning(t *testing.T) {
	cp := cloudFake()
	d, err := NewCloud(cp, "app").Load(context.Background(), "ghost", time.Time{})
	if err != nil || len(d.Promotions) != 0 || len(d.Warnings) != 1 {
		t.Fatalf("err=%v d=%+v", err, d)
	}
	for _, c := range cp.calls {
		if c == procListPromotions {
			t.Error("must not list promotions of an environment that does not exist")
		}
	}
}

func TestCloud_SecondaryReadsFailSoft_PrimaryFailsHard(t *testing.T) {
	cp := cloudFake()
	for _, p := range []string{procListConvergences, procListActivity, procListReleases} {
		cp.respond[p] = func(map[string]any) (any, error) { return nil, errors.New("Unimplemented") }
	}
	d, err := NewCloud(cp, "").Load(context.Background(), "prod", time.Time{})
	if err != nil || len(d.Promotions) != 3 || len(d.Warnings) != 3 {
		t.Fatalf("err=%v promotions=%d warnings=%v", err, len(d.Promotions), d.Warnings)
	}
	if tl := Assemble(d, time.Time{}, 0); tl.Entries[0].Direction != DirUnknown {
		t.Errorf("without releases the direction is unknown, got %q", tl.Entries[0].Direction)
	}

	cp = cloudFake()
	cp.respond[procListPromotions] = func(map[string]any) (any, error) { return nil, errors.New("permission denied") }
	if _, err := (NewCloud(cp, "")).Load(context.Background(), "prod", time.Time{}); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("err = %v", err)
	}
}

func TestCloud_AmbiguousEnvironmentIsRefused(t *testing.T) {
	cp := cloudFake()
	cp.respond[procListEnvironments] = func(map[string]any) (any, error) {
		return map[string]any{"environments": []map[string]any{{"id": "a", "name": "prod"}, {"id": "b", "name": "prod"}}}, nil
	}
	if _, err := (NewCloud(cp, "")).Load(context.Background(), "prod", time.Time{}); err == nil || !strings.Contains(err.Error(), "refusing to guess") {
		t.Errorf("err = %v", err)
	}
}

func TestCloud_Scopes(t *testing.T) {
	if !reflect.DeepEqual(Scopes, []string{"deploy:read", "obs:read"}) {
		t.Errorf("Scopes = %v", Scopes)
	}
}

// ── git ─────────────────────────────────────────────────────────────────────

func gitRepo(t *testing.T, subjects ...string) (dir string, shas []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir = t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=Ann", "GIT_AUTHOR_EMAIL=ann@example.com",
			"GIT_COMMITTER_NAME=Ann", "GIT_COMMITTER_EMAIL=ann@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	for i, s := range subjects {
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte(fmt.Sprint(i)), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "f")
		run("commit", "-q", "-m", s)
		shas = append(shas, run("rev-parse", "HEAD"))
	}
	return dir, shas
}

func TestGit_ListsARecordedRangeAndItsPullRequests(t *testing.T) {
	dir, shas := gitRepo(t, "base (#1)", "add a (#2)", "wip", "add b (#3)")
	g := NewGit(dir)
	r := CommitRange{FromCommit: shas[0], ToCommit: shas[3]}

	commits, prs, err := CommitsOf(context.Background(), g, r, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 3 || commits[0].SHA != shas[3] || commits[0].Author != "Ann" || commits[2].SHA != shas[1] {
		t.Errorf("commits = %+v", commits)
	}
	if !reflect.DeepEqual(prs, []int{3, 2}) {
		t.Errorf("prs = %v", prs)
	}

	// A BEHIND move takes away the commits between the two: the range is
	// read from the older commit up to the newer.
	rev := CommitRange{FromCommit: shas[3], ToCommit: shas[0], Reverts: true}
	commits, _, err = CommitsOf(context.Background(), g, rev, 0)
	if err != nil || len(commits) != 3 || commits[0].SHA != shas[3] {
		t.Errorf("reverted range: %v %+v", err, commits)
	}

	if commits, _, _ = CommitsOf(context.Background(), g, r, 2); len(commits) != 2 {
		t.Errorf("max not honoured: %d", len(commits))
	}
}

func TestGit_FirstReleaseListsTheWholeHistoryAndUnknownCommitsFail(t *testing.T) {
	dir, shas := gitRepo(t, "one", "two")
	commits, _, err := CommitsOf(context.Background(), NewGit(dir), CommitRange{ToCommit: shas[1]}, 0)
	if err != nil || len(commits) != 2 {
		t.Fatalf("err=%v commits=%d", err, len(commits))
	}
	if _, _, err := CommitsOf(context.Background(), NewGit(dir), CommitRange{ToCommit: sha('f')}, 0); err == nil {
		t.Error("a commit this checkout lacks must be an error, not an empty list")
	}
	if _, _, err := CommitsOf(context.Background(), NewGit(dir), CommitRange{FromCommit: "HEAD~1", ToCommit: shas[1]}, 0); err == nil {
		t.Error("a ref that is not a full object id must be refused")
	}
	if _, _, err := CommitsOf(context.Background(), NewGit(dir), CommitRange{}, 0); err == nil {
		t.Error("a range with no commit must be refused")
	}
}

func TestCompareReleases_ScopesToTheEnvironment(t *testing.T) {
	cp := cloudFake()
	cp.respond[procCompareReleases] = func(map[string]any) (any, error) {
		return map[string]any{"unchangedArtifacts": 2}, nil
	}
	var out struct {
		Unchanged int `json:"unchangedArtifacts"`
	}
	if err := CompareReleases(context.Background(), NewCloud(cp, "app"), "v1", "v2", "prod", &out); err != nil {
		t.Fatal(err)
	}
	req := cp.reqs[procCompareReleases]
	if out.Unchanged != 2 || req["fromVersion"] != "v1" || req["toVersion"] != "v2" || req["environmentId"] != "env-1" || req["project"] != "app" {
		t.Errorf("out=%+v req=%v", out, req)
	}
}

func TestGit_ChangedPathsListsTheMigrationsARangeCarried(t *testing.T) {
	dir, shas := gitRepo(t, "base", "next")
	add := func(rel, body string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	add("db/migrations/20261001_a.up.sql", "select 1;")
	add("other/x.txt", "x")
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "carry a migration (#5)"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=Ann", "GIT_AUTHOR_EMAIL=a@example.com", "GIT_COMMITTER_NAME=Ann", "GIT_COMMITTER_EMAIL=a@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	to := strings.TrimSpace(string(head))

	got, err := NewGit(dir).ChangedPaths(context.Background(), shas[1], to, "db/migrations")
	if err != nil || !reflect.DeepEqual(got, []PathChange{{Status: "A", Path: "db/migrations/20261001_a.up.sql"}}) {
		t.Errorf("got %+v, err %v", got, err)
	}
	if got, err := (NewGit(dir)).ChangedPaths(context.Background(), "", to, "db/migrations"); err != nil || got != nil {
		t.Errorf("a first release has nothing to differ from: %+v %v", got, err)
	}
	if _, err := (NewGit(dir)).ChangedPaths(context.Background(), "HEAD~1", to); err == nil {
		t.Error("a ref that is not a full object id must be refused")
	}
}

func TestTraceStart_IsTheEarliestSpanAndScopedToTheEnvironment(t *testing.T) {
	cp := cloudFake()
	cp.respond[procGetTrace] = func(map[string]any) (any, error) {
		return map[string]any{"spans": []map[string]any{
			{"startTime": "2026-10-01T12:45:07Z"}, {"startTime": "2026-10-01T12:45:05.5Z"}, {"startTime": "2026-10-01T12:45:06Z"},
		}}, nil
	}
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	got, err := TraceStart(context.Background(), NewCloud(cp, "app"), "prod", "ABCDEF0123456789abcdef0123456789", now)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(time.Date(2026, 10, 1, 12, 45, 5, 500_000_000, time.UTC)) {
		t.Errorf("start = %v", got)
	}
	req := cp.reqs[procGetTrace]
	scope, _ := req["scope"].(map[string]any)
	if req["traceId"] != "abcdef0123456789abcdef0123456789" || scope["orgId"] != "org-1" || scope["projectId"] != "app" {
		t.Errorf("req = %v", req)
	}
	if ids, _ := scope["envIds"].([]any); len(ids) != 1 || ids[0] != "env-1" {
		t.Errorf("envIds = %v", scope["envIds"])
	}
	tr, _ := scope["timeRange"].(map[string]any)
	start, _ := time.Parse(time.RFC3339, tr["start"].(string))
	if now.Sub(start) > 7*24*time.Hour {
		t.Errorf("range %v..%v exceeds the 7 day maximum", tr["start"], tr["end"])
	}
}

func TestTraceStart_UnknownTraceAndBadIDs(t *testing.T) {
	cp := cloudFake()
	cp.respond[procGetTrace] = func(map[string]any) (any, error) { return map[string]any{"spans": []any{}}, nil }
	_, err := TraceStart(context.Background(), NewCloud(cp, ""), "prod", "abc123", time.Now())
	if !errors.Is(err, ErrTraceNotFound) {
		t.Errorf("err = %v", err)
	}
	if _, err := TraceStart(context.Background(), NewCloud(cp, ""), "prod", "not hex!", time.Now()); err == nil {
		t.Error("a malformed trace id must be refused before any call")
	}
	if _, err := TraceStart(context.Background(), NewCloud(cp, ""), "ghost", "abc123", time.Now()); err == nil {
		t.Error("an unknown environment must be an error")
	}
}

func TestReleaseHealth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resp    map[string]any
		want    Health
		wantErr bool
	}{
		{"pass", map[string]any{"verdict": "RELEASE_VERDICT_PASS", "afterComplete": true}, Health{Verdict: "pass"}, false},
		{"fail names its first finding", map[string]any{"verdict": "RELEASE_VERDICT_FAIL", "findings": []map[string]any{{"message": "error rate 4x"}, {"message": "b"}}},
			Health{Verdict: "fail", Detail: "error rate 4x (+1 more)"}, false},
		{"inconclusive while the window is open", map[string]any{"verdict": "RELEASE_VERDICT_INCONCLUSIVE"},
			Health{Verdict: "inconclusive", Detail: "the after window is still open"}, false},
		{"no verdict is not a verdict", map[string]any{"verdict": "RELEASE_VERDICT_UNSPECIFIED"}, Health{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := cloudFake()
			cp.respond[procGetReleaseHealth] = func(map[string]any) (any, error) { return tc.resp, nil }
			got, err := ReleaseHealth(context.Background(), NewCloud(cp, ""), "prod", "p3")
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("got %+v, %v; want %+v (err=%v)", got, err, tc.want, tc.wantErr)
			}
			if cp.reqs[procGetReleaseHealth]["promotionId"] != "p3" {
				t.Errorf("req = %v", cp.reqs[procGetReleaseHealth])
			}
		})
	}
	cp := cloudFake()
	cp.respond[procGetReleaseHealth] = func(map[string]any) (any, error) { return nil, errors.New("unimplemented") }
	if _, err := ReleaseHealth(context.Background(), NewCloud(cp, ""), "prod", "p3"); err == nil {
		t.Error("an Unimplemented control plane must surface as an error the caller can soften")
	}
}
