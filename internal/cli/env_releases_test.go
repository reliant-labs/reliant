package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/obsout"
	"github.com/reliant-labs/forge/internal/releasetimeline"
	"github.com/reliant-labs/forge/pkg/release"
)

var relNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func relSHA(c byte) string { return strings.Repeat(string(c), 40) }

// fakeLister serves commits from a table keyed "from..to".
type fakeLister struct {
	commits map[string][]release.Commit
	err     error
}

func (f fakeLister) Commits(_ context.Context, from, to string, _ int) ([]release.Commit, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.commits[from+".."+to], nil
}

// localDeps builds deps over a real machine ledger holding v1 <- v2 <- v3,
// promoted v1, v2, v3, then rolled back to v1 (one failed apply on v2).
func localDeps(t *testing.T) (releasesDeps, []release.Promotion) {
	t.Helper()
	store, err := ledgerfile.Open(t.TempDir(), "proj-abc123")
	if err != nil {
		t.Fatal(err)
	}
	base := relNow.Add(-6 * time.Hour)
	art := func(c byte) map[string]release.Artifact {
		return map[string]release.Artifact{"ghcr.io/acme/api": {
			Kind: release.KindOCI, Mode: release.ModeShared,
			Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat(string(c), 64)},
		}}
	}
	cut := func(v string, min int, commit byte, baseV string, baseC byte, digest byte) {
		r := release.Release{Version: v, CreatedAt: base.Add(time.Duration(min) * time.Minute), Git: release.Git{Commit: relSHA(commit)}, Artifacts: art(digest)}
		if baseV != "" {
			r.Commits = &release.CommitRange{BaseRelease: baseV, BaseCommit: relSHA(baseC), Total: 1,
				Commits: []release.Commit{{SHA: relSHA(commit), Subject: "x (#" + v[1:] + "0)"}}}
		}
		if _, err := store.CutRelease(r); err != nil {
			t.Fatal(err)
		}
	}
	cut("v1", 0, 'a', "", 0, 'a')
	cut("v2", 10, 'b', "v1", 'a', 'b')
	cut("v3", 20, 'c', "v2", 'b', 'c')

	// ImportPromotion keeps the timestamps (AppendPromotion stamps "now", which
	// would put four promotions in one second): v1, v2, v3, then back to v1.
	var promos []release.Promotion
	for i, v := range []string{"v1", "v2", "v3", "v1"} {
		p := release.Promotion{
			ID: fmt.Sprintf("p%d", i+1), Env: "prod", Release: v, Kind: release.KindPromote,
			Resolved: map[string]string{"api": "sha256:" + strings.Repeat("d", 64)}, PromotedBy: release.Actor{Actor: "ci"},
			Run:        release.Run{ID: "gh/" + v, Provider: "github"},
			PromotedAt: relNow.Add(-time.Duration(300-i*60) * time.Minute).Truncate(time.Second),
		}
		if err := store.ImportPromotion(p); err != nil {
			t.Fatal(err)
		}
		promos = append(promos, p)
	}
	if err := store.AppendGates("prod", promos[1].ID, []release.Gate{{Name: "smoke", Status: release.GateStatusFailed, Summary: "502"}}); err != nil {
		t.Fatal(err)
	}
	a, err := store.BeginApply(release.Apply{Env: "prod", BundleID: "b", PromotionID: promos[1].ID,
		CreatedAt: promos[1].PromotedAt, DeadlineAt: promos[1].PromotedAt.Add(time.Hour)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishApply("prod", release.ApplyOutcome{ApplyID: a.ID, Status: release.ApplyFailed, Summary: "pods crashing",
		FinishedAt: promos[1].PromotedAt.Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}

	return releasesDeps{
		open: func(context.Context, string) (releasesBackend, error) {
			return releasesBackend{Name: backendLocal, Source: releasetimeline.Local{Ledger: store}, Ledger: store.Dir(), Bundle: store.Bundle}, nil
		},
		now: func() time.Time { return relNow },
		commits: func() releasetimeline.CommitLister {
			return fakeLister{commits: map[string][]release.Commit{
				relSHA('a') + ".." + relSHA('b'): {{SHA: relSHA('b'), Subject: "add b (#42)", Author: "Ann"}, {SHA: relSHA('e'), Subject: "wip", Author: "Bo"}},
				relSHA('c') + ".." + relSHA('a'): nil,
			}}
		},
	}, promos
}

func runReleases(t *testing.T, deps releasesDeps, args ...string) (string, error) {
	t.Helper()
	cmd := newEnvReleasesCmdWith(deps)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return out.String(), err
}

func decodeDoc(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	return m
}

func TestEnvReleasesList_JSONHasTheEnvelopeAndMarksTheRollback(t *testing.T) {
	deps, promos := localDeps(t)
	out, err := runReleases(t, deps, "list", "prod", "--json", "--since", "30d")
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, out)
	if doc["schema"] != schemaReleasesList || doc["ok"] != true || doc["exit_code"] != float64(0) || doc["backend"] != "local" {
		t.Errorf("head = %v %v %v %v", doc["schema"], doc["ok"], doc["exit_code"], doc["backend"])
	}
	entries := doc["entries"].([]any)
	// 4 promotions + 1 failed deploy, newest first.
	if len(entries) != 5 {
		t.Fatalf("entries = %d: %s", len(entries), out)
	}
	newest := entries[0].(map[string]any)
	if newest["promotion_id"] != promos[3].ID || newest["direction"] != "behind" || newest["live"] != true || newest["previous_version"] != "v3" {
		t.Errorf("newest = %v", newest)
	}
	kinds := map[string]int{}
	for _, e := range entries {
		kinds[e.(map[string]any)["kind"].(string)]++
	}
	if kinds["failed_deploy"] != 1 || kinds["promotion"] != 4 {
		t.Errorf("kinds = %v", kinds)
	}
	if doc["truncated"] != false {
		t.Errorf("truncated = %v", doc["truncated"])
	}
	// Suggestions are complete, runnable commands, at most three.
	sug := doc["suggested"].([]any)
	if len(sug) == 0 || len(sug) > obsout.MaxSuggested {
		t.Fatalf("suggested = %v", sug)
	}
	for _, s := range sug {
		if !strings.HasPrefix(s.(string), releasesCmd("env", "releases", "show", "prod")+" ") {
			t.Errorf("suggestion %q is not a runnable show command", s)
		}
	}
}

func TestEnvReleasesList_LimitSinceAndFields(t *testing.T) {
	deps, _ := localDeps(t)
	out, err := runReleases(t, deps, "list", "prod", "--json", "--limit", "2", "--fields", "entries.version,entries.direction")
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, out)
	if doc["truncated"] != true {
		t.Errorf("a capped list must say so: %v", doc["truncated"])
	}
	for _, e := range doc["entries"].([]any) {
		m := e.(map[string]any)
		if len(m) > 2 {
			t.Errorf("--fields kept %v", m)
		}
	}
	if doc["schema"] != schemaReleasesList || doc["ok"] != true {
		t.Errorf("--fields must keep the head: %v", doc)
	}

	// A window with nothing in it is a successful read.
	deps.now = func() time.Time { return relNow.Add(400 * 24 * time.Hour) }
	out, err = runReleases(t, deps, "list", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeDoc(t, out)["entries"].([]any); len(got) != 0 {
		t.Errorf("entries = %v", got)
	}
}

func TestEnvReleasesList_BadInputIsExit1AndAFailureDocument(t *testing.T) {
	deps, _ := localDeps(t)
	for _, args := range [][]string{
		{"list", "prod", "--json", "--fields", "entries.nope"},
		{"list", "prod", "--json", "--since", "yesterday-ish"},
		{"list", "prod", "--json", "--limit", "99999"},
	} {
		out, err := runReleases(t, deps, args...)
		if err == nil || exitCodeForError(err) != exitWrong {
			t.Errorf("%v: err=%v code=%d", args, err, exitCodeForError(err))
			continue
		}
		if strings.Contains(args[len(args)-2], "fields") {
			continue // rejected before any document is written
		}
		doc := decodeDoc(t, out)
		if doc["ok"] != false || doc["exit_code"] != float64(exitWrong) || doc["error"] == "" {
			t.Errorf("%v: failure document = %v", args, doc)
		}
	}
}

func TestEnvReleasesList_AnUnreachableLedgerIsExit2(t *testing.T) {
	deps, _ := localDeps(t)
	deps.open = func(context.Context, string) (releasesBackend, error) {
		return releasesBackend{}, undeterminedf("reach the control plane of prod: no credential")
	}
	out, err := runReleases(t, deps, "list", "prod", "--json")
	if err == nil || exitCodeForError(err) != exitUndetermined {
		t.Fatalf("err=%v code=%d", err, exitCodeForError(err))
	}
	doc := decodeDoc(t, out)
	if doc["exit_code"] != float64(exitUndetermined) || doc["ok"] != false {
		t.Errorf("document = %v", doc)
	}
}

func TestEnvReleasesList_HumanTableNamesTheRollbackAndTheFailure(t *testing.T) {
	deps, _ := localDeps(t)
	out, err := runReleases(t, deps, "list", "prod", "--since", "30d")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ROLLBACK (behind v3)", "DEPLOY FAILED", "v1 (live)", "ci via github", "first promote", "failed (1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
}

func TestEnvReleasesShow_CommitsPRsAndChangesForALocalPromotion(t *testing.T) {
	deps, promos := localDeps(t)
	out, err := runReleases(t, deps, "show", "prod", promos[1].ID, "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, out)
	entry := doc["entry"].(map[string]any)
	if entry["version"] != "v2" || entry["direction"] != "ahead" || entry["gate_verdict"] != "failed" {
		t.Errorf("entry = %v", entry)
	}
	commits := doc["commits"].([]any)
	if len(commits) != 2 || commits[0].(map[string]any)["pr"] != float64(42) {
		t.Errorf("commits = %v", commits)
	}
	if prs := doc["pull_requests"].([]any); len(prs) != 1 || prs[0] != float64(42) {
		t.Errorf("pull_requests = %v", prs)
	}
	failed := doc["failed_deploys"].([]any)
	if len(failed) != 1 || failed[0].(map[string]any)["rollout"].(map[string]any)["reason"] != "pods crashing" {
		t.Errorf("failed_deploys = %v", failed)
	}
	ch := doc["changes"].(map[string]any)
	arts := ch["artifacts"].([]any)
	if len(arts) != 1 || arts[0].(map[string]any)["name"] != "ghcr.io/acme/api" || arts[0].(map[string]any)["change"] != "changed" {
		t.Errorf("changes = %v", ch)
	}
	// No bundles were recorded, so objects are not compared and the note says why.
	if note, _ := doc["changes_note"].(string); !strings.Contains(note, "bundle") {
		t.Errorf("changes_note = %q", note)
	}
}

func TestEnvReleasesShow_ARollbackListsWhatItTakesAway(t *testing.T) {
	deps, promos := localDeps(t)
	var asked string
	deps.commits = func() releasetimeline.CommitLister {
		return listerFunc(func(from, to string) ([]release.Commit, error) {
			asked = from + ".." + to
			return []release.Commit{{SHA: relSHA('c'), Subject: "add c (#7)"}}, nil
		})
	}
	out, err := runReleases(t, deps, "show", "prod", promos[3].ID, "--json")
	if err != nil {
		t.Fatal(err)
	}
	// v3 -> v1 is BEHIND: git is asked for v1..v3, the commits the move removes.
	if asked != relSHA('a')+".."+relSHA('c') {
		t.Errorf("git asked for %q", asked)
	}
	doc := decodeDoc(t, out)
	if entry := doc["entry"].(map[string]any); entry["direction"] != "behind" {
		t.Errorf("entry = %v", entry)
	}
	if len(doc["commits"].([]any)) != 1 {
		t.Errorf("commits = %v", doc["commits"])
	}
}

type listerFunc func(from, to string) ([]release.Commit, error)

func (f listerFunc) Commits(_ context.Context, from, to string, _ int) ([]release.Commit, error) {
	return f(from, to)
}

func TestEnvReleasesShow_GitFailureIsANoteNotAnError(t *testing.T) {
	deps, promos := localDeps(t)
	deps.commits = func() releasetimeline.CommitLister { return fakeLister{err: errors.New("bad object")} }
	out, err := runReleases(t, deps, "show", "prod", "v2", "--json") // by version
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, out)
	if note, _ := doc["commits_note"].(string); !strings.Contains(note, "bad object") {
		t.Errorf("commits_note = %q", note)
	}
	if doc["entry"].(map[string]any)["promotion_id"] != promos[1].ID {
		t.Errorf("by-version must resolve the newest promotion of v2")
	}
}

func TestEnvReleasesShow_UnknownRefIsExit1(t *testing.T) {
	deps, _ := localDeps(t)
	out, err := runReleases(t, deps, "show", "prod", "v99", "--json")
	if err == nil || exitCodeForError(err) != exitWrong {
		t.Fatalf("err=%v code=%d", err, exitCodeForError(err))
	}
	if doc := decodeDoc(t, out); doc["ok"] != false || !strings.Contains(doc["error"].(string), "v99") {
		t.Errorf("document = %v", doc)
	}
}

func TestEnvReleasesShow_HumanFormShowsGatesCommitsAndPRs(t *testing.T) {
	deps, promos := localDeps(t)
	out, err := runReleases(t, deps, "show", "prod", promos[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"v2", "gates (failed)", "smoke", "502", "add b (#42)", "PRs: #42", "DEPLOY FAILED", "after 2m0s", "pods crashing", "changed vs v1", "artifact"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
}

func TestEnvReleasesAt_Brackets(t *testing.T) {
	deps, promos := localDeps(t)
	when := promos[2].PromotedAt // exactly on v3's promotion
	out, err := runReleases(t, deps, "at", "prod", when.Format(time.RFC3339), "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, out)
	if doc["schema"] != schemaReleasesAt || doc["resolved_from"] != "timestamp" {
		t.Errorf("head = %v", doc)
	}
	name := func(k string) any {
		if m, ok := doc[k].(map[string]any); ok {
			return m["version"]
		}
		return nil
	}
	if name("live") != "v3" || name("previous") != "v2" || name("next") != "v1" {
		t.Errorf("live/previous/next = %v/%v/%v", name("live"), name("previous"), name("next"))
	}

	// Before anything was promoted: answered, not an error.
	out, err = runReleases(t, deps, "at", "prod", "2020-01-01", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc = decodeDoc(t, out)
	if doc["before_history"] != true || doc["live"] != nil || doc["ok"] != true {
		t.Errorf("before the first release: %v", doc)
	}
}

func TestEnvReleasesAt_RelativeNowAndFuture(t *testing.T) {
	deps, _ := localDeps(t)
	out, err := runReleases(t, deps, "at", "prod", "now", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if live := decodeDoc(t, out)["live"].(map[string]any); live["version"] != "v1" || live["live"] != true {
		t.Errorf("live now = %v", live)
	}
	if _, err := runReleases(t, deps, "at", "prod", "2027-01-01", "--json"); err == nil || exitCodeForError(err) != exitWrong {
		t.Errorf("a future time must be refused: %v", err)
	}
	if _, err := runReleases(t, deps, "at", "prod", "garbage", "--json"); err == nil || exitCodeForError(err) != exitWrong {
		t.Errorf("a non-time must be refused: %v", err)
	}
}

func TestEnvReleasesAt_ATraceIDNeedsAControlPlane(t *testing.T) {
	deps, _ := localDeps(t) // local backend
	out, err := runReleases(t, deps, "at", "prod", "0123456789abcdef0123456789abcdef", "--json")
	if err == nil || exitCodeForError(err) != exitWrong {
		t.Fatalf("err=%v", err)
	}
	if doc := decodeDoc(t, out); !strings.Contains(doc["error"].(string), "trace id") {
		t.Errorf("document = %v", doc)
	}
}

// ── cloud backend, through a fake control plane ─────────────────────────────

type cloudStub struct {
	calls   []string
	respond map[string]func(req map[string]any) (any, error)
}

func (c *cloudStub) Call(_ context.Context, proc string, req, out any) error {
	c.calls = append(c.calls, proc)
	raw, _ := json.Marshal(req)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	fn, ok := c.respond[proc]
	if !ok {
		return fmt.Errorf("unimplemented: %s", proc)
	}
	resp, err := fn(m)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(resp)
	return json.Unmarshal(body, out)
}

func cloudDeps(stub *cloudStub) releasesDeps {
	cl := releasetimeline.NewCloud(stub, "app")
	return releasesDeps{
		open: func(context.Context, string) (releasesBackend, error) {
			return releasesBackend{Name: backendCloud, Source: cl, Cloud: &cl, Ledger: "https://cp.example"}, nil
		},
		now: func() time.Time { return relNow },
		commits: func() releasetimeline.CommitLister {
			return fakeLister{commits: map[string][]release.Commit{relSHA('a') + ".." + relSHA('b'): {{SHA: relSHA('b'), Subject: "add b (#42)"}}}}
		},
	}
}

func newCloudStub() *cloudStub {
	at := func(min int) string { return relNow.Add(-time.Duration(min) * time.Minute).Format(time.RFC3339) }
	return &cloudStub{respond: map[string]func(map[string]any) (any, error){
		"controlplane.v1.DeployService/ListEnvironments": func(map[string]any) (any, error) {
			return map[string]any{"environments": []map[string]any{{"id": "env-1", "name": "prod", "project": "app"}}}, nil
		},
		"controlplane.v1.DeployService/ListPromotions": func(map[string]any) (any, error) {
			return map[string]any{"promotions": []map[string]any{
				{"id": "p2", "bundleId": "b2", "releaseVersion": "v2", "createdAt": at(30), "promotedByActor": "ci"},
				{"id": "p1", "bundleId": "b1", "releaseVersion": "v1", "createdAt": at(90), "promotedByActor": "ci"},
			}}, nil
		},
		"controlplane.v1.DeployService/ListReleases": func(map[string]any) (any, error) {
			return map[string]any{"releases": []map[string]any{
				{"version": "v1", "gitCommit": relSHA('a'), "createdAt": at(200)},
				{"version": "v2", "gitCommit": relSHA('b'), "createdAt": at(100), "commits": map[string]any{"baseRelease": "v1", "baseCommit": relSHA('a'), "total": 1,
					"commits": []map[string]any{{"sha": relSHA('b'), "subject": "add b (#42)", "time": at(101)}}}},
			}}, nil
		},
		"controlplane.v1.DeployService/ListConvergences": func(map[string]any) (any, error) {
			return map[string]any{"convergences": []map[string]any{
				{"id": "c2", "promotionId": "p2", "state": "converged", "observedAt": at(25), "healthyAt": at(26), "firstAttemptAt": at(30)},
			}}, nil
		},
		cloud.ProcGetOrganization: func(map[string]any) (any, error) {
			return cloud.OrganizationReply("org-1"), nil
		},
		"controlplane.v1.ActivityService/ListActivity": func(map[string]any) (any, error) {
			return map[string]any{"events": []map[string]any{{"at": at(30), "verb": "promote", "actor": map[string]any{"name": "alice"}, "via": "console",
				"target": map[string]any{"id": "v2"}, "detail": map[string]any{"promotion_id": "p2"}}}}, nil
		},
		"controlplane.v1.ObservabilityService/GetReleaseHealth": func(req map[string]any) (any, error) {
			if req["promotionId"] == "p2" {
				return map[string]any{"verdict": "RELEASE_VERDICT_FAIL", "findings": []map[string]any{{"message": "error rate up 4x"}}}, nil
			}
			return map[string]any{"verdict": "RELEASE_VERDICT_PASS", "afterComplete": true}, nil
		},
		"controlplane.v1.ObservabilityService/GetTrace": func(map[string]any) (any, error) {
			return map[string]any{"spans": []map[string]any{{"startTime": at(40)}, {"startTime": at(39)}}}, nil
		},
		"controlplane.v1.DeployService/CompareReleases": func(req map[string]any) (any, error) {
			return map[string]any{
				"artifacts":          []map[string]any{{"name": "api", "change": "DEPLOY_CHANGE_KIND_MODIFIED", "from": "sha256:" + strings.Repeat("a", 64), "to": "sha256:" + strings.Repeat("b", 64)}},
				"unchangedArtifacts": 3,
				"objects":            map[string]any{"deltas": []map[string]any{{"change": "DEPLOY_CHANGE_KIND_MODIFIED", "cluster": "c", "kind": "Deployment", "namespace": "app", "name": "api", "summary": "image moved"}}, "unchanged": 9},
			}, nil
		},
	}}
}

func TestEnvReleasesList_CloudCarriesWhoViaAndHealth(t *testing.T) {
	out, err := runReleases(t, cloudDeps(newCloudStub()), "list", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, out)
	if doc["backend"] != "cloud" || doc["ledger"] != "https://cp.example" {
		t.Errorf("backend = %v %v", doc["backend"], doc["ledger"])
	}
	e := doc["entries"].([]any)[0].(map[string]any)
	if e["version"] != "v2" || e["actor"] != "alice" || e["via"] != "console" {
		t.Errorf("newest = %v", e)
	}
	h := e["health"].(map[string]any)
	if h["verdict"] != "fail" || h["detail"] != "error rate up 4x" {
		t.Errorf("health = %v", h)
	}
	if rollout := e["rollout"].(map[string]any); rollout["status"] != "succeeded" || rollout["duration_s"] != float64(240) {
		t.Errorf("rollout = %v", rollout)
	}
}

func TestEnvReleasesList_AControlPlaneWithoutHealthStillListsAndWarns(t *testing.T) {
	stub := newCloudStub()
	delete(stub.respond, "controlplane.v1.ObservabilityService/GetReleaseHealth")
	out, err := runReleases(t, cloudDeps(stub), "list", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, out)
	if _, has := doc["entries"].([]any)[0].(map[string]any)["health"]; has {
		t.Error("an unavailable verdict must be absent, never invented")
	}
	warned := false
	for _, w := range doc["warnings"].([]any) {
		warned = warned || strings.Contains(w.(string), "no health verdicts")
	}
	if !warned {
		t.Errorf("warnings = %v", doc["warnings"])
	}
}

func TestEnvReleasesAt_ATraceIDResolvesThroughTheControlPlane(t *testing.T) {
	out, err := runReleases(t, cloudDeps(newCloudStub()), "at", "prod", "0123456789ABCDEF0123456789abcdef", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, out)
	if doc["resolved_from"] != "trace" {
		t.Errorf("resolved_from = %v", doc["resolved_from"])
	}
	// The trace began 40 minutes ago: v1 (90 min ago) was live, v2 (30) came next.
	if live := doc["live"].(map[string]any); live["version"] != "v1" {
		t.Errorf("live = %v", live)
	}
	if next := doc["next"].(map[string]any); next["version"] != "v2" {
		t.Errorf("next = %v", next)
	}
}

func TestEnvReleasesAt_AnUnknownTraceIsExit1WithAHint(t *testing.T) {
	stub := newCloudStub()
	stub.respond["controlplane.v1.ObservabilityService/GetTrace"] = func(map[string]any) (any, error) { return map[string]any{"spans": []any{}}, nil }
	_, err := runReleases(t, cloudDeps(stub), "at", "prod", "0123456789abcdef", "--json")
	if err == nil || exitCodeForError(err) != exitWrong || !strings.Contains(err.Error(), "7 days") {
		t.Errorf("err=%v", err)
	}
}

func TestEnvReleasesShow_CloudUsesCompareReleases(t *testing.T) {
	out, err := runReleases(t, cloudDeps(newCloudStub()), "show", "prod", "v2", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, out)
	ch := doc["changes"].(map[string]any)
	art := ch["artifacts"].([]any)[0].(map[string]any)
	if art["change"] != "modified" || art["name"] != "api" || ch["unchanged_artifacts"] != float64(3) || ch["unchanged_objects"] != float64(9) {
		t.Errorf("changes = %v", ch)
	}
	obj := ch["objects"].([]any)[0].(map[string]any)
	if obj["object"] != "c/Deployment/app/api" || obj["summary"] != "image moved" {
		t.Errorf("objects = %v", ch["objects"])
	}
	if doc["entry"].(map[string]any)["health"].(map[string]any)["verdict"] != "fail" {
		t.Errorf("show must carry the health verdict: %v", doc["entry"])
	}
}

func TestEnvReleasesShow_ACompareFailureIsANoteNotAnError(t *testing.T) {
	stub := newCloudStub()
	delete(stub.respond, "controlplane.v1.DeployService/CompareReleases")
	out, err := runReleases(t, cloudDeps(stub), "show", "prod", "v2", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, out)
	if doc["changes"] != nil || !strings.Contains(doc["changes_note"].(string), "could not compare") {
		t.Errorf("document = %v", doc)
	}
}

func TestParseWhen(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Time
		bad  bool
	}{
		{"now", relNow, false},
		{"90m", relNow.Add(-90 * time.Minute), false},
		{"2d", relNow.Add(-48 * time.Hour), false},
		{"1.5d", relNow.Add(-36 * time.Hour), false},
		{"2026-10-21", time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), false},
		{"2026-10-21T14:30:00+02:00", time.Date(2026, 10, 21, 12, 30, 0, 0, time.UTC), false},
		{"-5m", time.Time{}, true},
		{"someday", time.Time{}, true},
		{"d", time.Time{}, true},
	} {
		got, err := parseWhen(tc.in, relNow, true)
		if (err != nil) != tc.bad || (!tc.bad && !got.Equal(tc.want)) {
			t.Errorf("parseWhen(%q) = %v, %v; want %v (bad=%v)", tc.in, got, err, tc.want, tc.bad)
		}
	}
	if got, err := parseSince("", relNow); err != nil || !got.IsZero() {
		t.Errorf("no --since is no bound: %v %v", got, err)
	}
}

func TestEnvReleasesIsRegisteredAndCloudReleasesIsGone(t *testing.T) {
	root := newTestRootCmd(t)
	if c, _, err := root.Find([]string{"env", "releases", "list"}); err != nil || c.Name() != "list" {
		t.Errorf("env releases list not registered: %v", err)
	}
	for _, name := range []string{"list", "show", "at"} {
		if c, _, err := root.Find([]string{"env", "releases", name}); err != nil || c.Name() != name {
			t.Errorf("env releases %s not registered", name)
		}
	}
	if c, _, _ := root.Find([]string{"cloud", "releases"}); c != nil && c.Name() == "releases" {
		t.Error("`forge cloud releases` is superseded by `forge env releases` and must be gone")
	}
}
