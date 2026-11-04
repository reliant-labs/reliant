package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavedPlanIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("real git processes; runs in full scoped tests")
	}
	root := t.TempDir()
	r := &orderRepo{t: t}
	r.git(root, "init", "-q")
	input := filepath.Join(root, "source.go")
	if err := os.WriteFile(input, []byte("package source\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r.git(root, "add", ".")
	r.git(root, "commit", "-qm", "fixture")
	head := strings.TrimSpace(r.git(root, "rev-parse", "HEAD"))
	ctx := context.Background()
	digest, err := checkoutDigest(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	pl := Plan{Schema: planSchema, Base: head, Head: head, InputDigest: digest}
	file := filepath.Join(t.TempDir(), "plan.json")
	save := func(p Plan) {
		t.Helper()
		b, e := json.Marshal(p)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(file, b, 0644); e != nil {
			t.Fatal(e)
		}
	}
	save(pl)
	if _, err := readPlan(ctx, root, head, file); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"{}", "null", "{", `{"schema":999}`, `{"unknown":true}`} {
		if err := os.WriteFile(file, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := readPlan(ctx, root, head, file); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, field := range []string{"base", "head", "digest", "schema"} {
		bad := pl
		switch field {
		case "base":
			bad.Base = "stale"
		case "head":
			bad.Head = "stale"
		case "digest":
			bad.InputDigest = strings.Repeat("0", 64)
		case "schema":
			bad.Schema = 99
		}
		save(bad)
		if _, err := readPlan(ctx, root, head, file); err == nil {
			t.Fatalf("accepted stale %s", field)
		}
	}
	save(pl)
	if err := os.WriteFile(input, []byte("package changed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPlan(ctx, root, head, file); err == nil {
		t.Fatal("accepted changed bytes at same head")
	}
	// The closing CLI also refuses stale evidence, even with every row green.
	dir := gateDir(t, &pl, map[string]int{"build": 0, "test": 0})
	var out strings.Builder
	if runGateVerify([]string{"-root", root, "-base", head, "-plan", filepath.Join(dir, "plan.json"), "-dir", dir}, &out, &out) == 0 {
		t.Fatal("closing verifier accepted stale source")
	}
	if err := os.WriteFile(input, []byte("package source\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPlan(ctx, root, head, file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package source\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPlan(ctx, root, head, file); err == nil {
		t.Fatal("accepted new untracked input")
	}
}

func TestRegenerationExpandedPlanIsAuthoritative(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "merge-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`run_plan=$L/plan-2.json`, `load_selection || fail=1`, `prepare_dependencies`, `regeneration expanded generator obligations`, `gate-verify -plan "$run_plan" -base "$base"`, `run -plan "$run_plan"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing authoritative plan consumer: %s", want)
		}
	}
	expanded := &Plan{Web: WebPlan{LaneFull: selected("regenerated web")}, DocGen: DocGenPlan{LaneFull: selected("new docs input")}}
	dir := gateDir(t, expanded, map[string]int{"build": 0, "test": 0})
	problems := strings.Join(verifyGate(filepath.Join(dir, "plan.json"), dir), ";")
	if !strings.Contains(problems, "typecheck never completed") || !strings.Contains(problems, "docs never completed") {
		t.Fatalf("expanded obligations lost: %s", problems)
	}
}
