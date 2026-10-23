package cli

// Tests for the promotion-ledger reader behind `forge env status <env>
// --history` (hosted-deploy-primitives §3.5, task F7). The hosted cases run
// the real hostedStore over cloud.Client against fakeDeployService, whose
// ListPromotions applies C7's keyset cursor and version filter.
//
// These exercise the READER, which both backends implement identically. The
// --history command surface itself is tested in env_status_cmd_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// pageAll walks a history to the end, the way a reader is told to: stop on an
// empty cursor, not on a short page.
func pageAll(t *testing.T, r bindingHistoryReader, env string, q historyQuery) [][]string {
	t.Helper()
	var pages [][]string
	for guard := 0; guard < 20; guard++ {
		page, err := r.HistoryPage(context.Background(), env, q)
		if err != nil {
			t.Fatalf("history page: %v", err)
		}
		var rels []string
		for _, p := range page.Promotions {
			rels = append(rels, p.Release)
		}
		pages = append(pages, rels)
		if page.Next == "" {
			return pages
		}
		q.Before = page.Next
	}
	t.Fatal("history did not terminate")
	return nil
}

// Both backends page the SAME way: newest first, keyset cursor, empty cursor
// on the last page. A reader written against one must work on the other.
func TestHistoryPage_BothBackendsPageIdentically(t *testing.T) {
	// File: prod v1 → v2 → v3 → v1 → v4.
	dir := t.TempDir()
	file := testBindings(t, dir)
	for _, v := range []string{"v1", "v2", "v3", "v1", "v4"} {
		writeBinding(t, dir, "prod", v, map[string]string{"api": sha("a")})
	}
	// Hosted: the same sequence through the Promote RPC.
	_, hosted := hostedPromoteFixture(t, "v1", "v2", "v3")
	for _, v := range []string{"v1", "v4"} {
		if v == "v4" {
			r := ociRelease("v4", map[string]string{"api": sha("4")})
			if _, err := hosted.Cut(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := hosted.Append(context.Background(), release.Promotion{Env: "prod", Release: v, Kind: release.KindPromote}, appendGuard{}); err != nil {
			t.Fatal(err)
		}
	}

	want := [][]string{{"v4", "v1"}, {"v3", "v2"}, {"v1"}}
	for name, r := range map[string]bindingHistoryReader{"file": file, "hosted": hosted} {
		got := pageAll(t, r, "prod", historyQuery{Limit: 2})
		if !equalPages(got, want) {
			t.Errorf("%s pages = %v, want %v", name, got, want)
		}
		// The version filter keeps only v1's two promotions, newest first.
		page, err := r.HistoryPage(context.Background(), "prod", historyQuery{Release: "v1"})
		if err != nil || len(page.Promotions) != 2 || page.Next != "" {
			t.Errorf("%s --release v1 = %+v %v, want 2 entries and no next", name, page, err)
		}
	}
}

func equalPages(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.Join(a[i], ",") != strings.Join(b[i], ",") {
			return false
		}
	}
	return true
}

// A cursor that names no promotion of this env: the file ledger refuses it
// (exit 1 — the request, not the ledger), so a pipeline paging with a typo
// does not loop over page one forever.
func TestEnvHistory_BadRequestIsExit1(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1", map[string]string{"api": sha("a")})
	store := testBindings(t, dir)
	for name, q := range map[string]historyQuery{
		"unknown cursor": {Before: "no-such-id"},
		"limit too big":  {Limit: maxHistoryLimit + 1},
	} {
		var buf bytes.Buffer
		err := runEnvHistory(context.Background(), store, "prod", q, false, &buf)
		if got := exitCodeForError(err); got != exitWrong {
			t.Errorf("%s: exit %d, want %d (%v)", name, got, exitWrong, err)
		}
	}
}

// --json carries the ledger entries with both evidence halves counted, the
// cursor, and the envelope. The capture recipe the CI template uses works.
func TestEnvHistory_JSON(t *testing.T) {
	dir := t.TempDir()
	store := testBindings(t, dir)
	writeBinding(t, dir, "prod", "v1", map[string]string{"api": sha("a")})
	if _, err := store.Append(context.Background(), release.Promotion{
		Env: "prod", Release: "v2", Kind: release.KindPromote, FromEnv: "staging", Note: "hotfix",
		Resolved: map[string]string{"api": sha("b")},
		Gates: []release.Gate{
			{Name: "lint", Status: release.GateStatusPassed},
			{Name: "test", Status: release.GateStatusFailed},
		},
	}, appendGuard{}); err != nil {
		t.Fatal(err)
	}

	var err error
	out := captureStdout(t, func() {
		err = runEnvHistory(context.Background(), store, "prod", historyQuery{Limit: 1}, true, &bytes.Buffer{})
	})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var doc struct {
		OK         bool `json:"ok"`
		Promotions []struct {
			ID      string        `json:"id"`
			Release string        `json:"release"`
			FromEnv string        `json:"from_env"`
			Note    string        `json:"note"`
			Gates   *gatesSummary `json:"gates_summary"`
		} `json:"promotions"`
		NextBefore string `json:"next_before"`
	}
	if jerr := json.Unmarshal([]byte(out), &doc); jerr != nil {
		t.Fatalf("decode: %v\n%s", jerr, out)
	}
	if !doc.OK || len(doc.Promotions) != 1 {
		t.Fatalf("doc = %+v", doc)
	}
	p := doc.Promotions[0]
	if p.Release != "v2" || p.FromEnv != "staging" || p.Note != "hotfix" || p.ID == "" {
		t.Errorf("newest entry = %+v", p)
	}
	if p.Gates == nil || p.Gates.Passed != 1 || p.Gates.Failed != 1 {
		t.Errorf("gates_summary = %+v, want 1 passed 1 failed", p.Gates)
	}
	if doc.NextBefore != p.ID {
		t.Errorf("next_before = %q, want the last served id %q (one more entry remains)", doc.NextBefore, p.ID)
	}
}
