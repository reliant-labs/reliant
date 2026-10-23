package main

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// assertPartition fails unless p assigns every test in selected to exactly
// one shard, assigns nothing else, and leaves no shard empty.
func assertPartition(t *testing.T, selected []string, p shardPlan) {
	t.Helper()
	want := map[string]bool{}
	for _, s := range selected {
		want[s] = true
	}
	seen := map[string]int{}
	for i, s := range p.shards {
		if len(s.tests) == 0 {
			t.Errorf("shard %d of %d is empty: it would pass having run nothing", i, len(p.shards))
		}
		for _, name := range s.tests {
			if !want[name] {
				t.Errorf("shard %d runs %s, which was not selected", i, name)
			}
			seen[name]++
		}
	}
	for name := range want {
		if seen[name] != 1 {
			t.Errorf("%s is in %d shards, want exactly 1", name, seen[name])
		}
	}
}

func TestPlanPartitionsAnySelection(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 200; iter++ {
		nTests := 1 + rng.Intn(120)
		shards := 1 + rng.Intn(nTests)
		if shards > 12 {
			shards = 12
		}
		tests := make([]string, nTests)
		timings := map[string]int{}
		for i := range tests {
			tests[i] = fmt.Sprintf("TestE2ECase%03d", i)
			// Some tests have no timing, some a non-positive one: both
			// fall back to the median and must still be planned.
			switch rng.Intn(5) {
			case 0:
			case 1:
				timings[tests[i]] = 0
			default:
				timings[tests[i]] = 1 + rng.Intn(500)
			}
		}
		p, err := plan(tests, timings, shards)
		if err != nil {
			t.Fatalf("iter %d: %v", iter, err)
		}
		assertPartition(t, tests, p)
	}
}

func TestPlanIsLongestFirstOntoTheLightestShard(t *testing.T) {
	timings := map[string]int{"TestE2EA": 100, "TestE2EB": 90, "TestE2EC": 60, "TestE2ED": 40, "TestE2EE": 10}
	p, err := plan([]string{"TestE2EA", "TestE2EB", "TestE2EC", "TestE2ED", "TestE2EE"}, timings, 2)
	if err != nil {
		t.Fatal(err)
	}
	// A(100)→0; B(90)→1; C(60)→1 (90<100); D(40)→0 (100<150); E(10)→0 (140<150).
	got := [][]string{p.shards[0].tests, p.shards[1].tests}
	want := [][]string{{"TestE2EA", "TestE2ED", "TestE2EE"}, {"TestE2EB", "TestE2EC"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	if p.shards[0].load != 150 || p.shards[1].load != 150 {
		t.Fatalf("loads = %d, %d; want 150, 150", p.shards[0].load, p.shards[1].load)
	}
}

func TestPlanBalancesBetterThanRoundRobin(t *testing.T) {
	// The committed timings are the real distribution; LPT must never do
	// worse than the `NR % n` split it replaced.
	timings, err := readTimings(filepath.Join("..", "..", ".github", "e2e-timings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tests []string
	for name := range timings {
		tests = append(tests, name)
	}
	sort.Strings(tests)
	for n := 2; n <= 6; n++ {
		p, err := plan(tests, timings, n)
		if err != nil {
			t.Fatal(err)
		}
		rr := make([]int, n)
		for i, name := range tests {
			rr[(i+1)%n] += timings[name]
		}
		if got, old := maxLoad(p), maxInts(rr); got > old {
			t.Errorf("%d shards: slowest planned shard %ds, round-robin's %ds", n, got, old)
		}
	}
}

func TestPlanFallsBackToTheMedianForUntimedTests(t *testing.T) {
	timings := map[string]int{"TestE2EA": 10, "TestE2EB": 30, "TestE2EC": 50}
	p, err := plan([]string{"TestE2EA", "TestE2EB", "TestE2EC", "TestE2ENew"}, timings, 2)
	if err != nil {
		t.Fatal(err)
	}
	if p.fallback != 30 {
		t.Errorf("fallback = %d, want the median 30", p.fallback)
	}
	if !reflect.DeepEqual(p.estimated, []string{"TestE2ENew"}) {
		t.Errorf("estimated = %v, want [TestE2ENew]", p.estimated)
	}
	if total := p.shards[0].load + p.shards[1].load; total != 120 {
		t.Errorf("total load %d, want 120 (the new test counted at 30)", total)
	}
	if md := p.markdown(selectPR, 4); !strings.Contains(md, "`TestE2ENew`") {
		t.Errorf("summary does not name the untimed test:\n%s", md)
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	tests := []string{"TestE2EA", "TestE2EB", "TestE2EC", "TestE2ED", "TestE2EE", "TestE2EF"}
	timings := map[string]int{"TestE2EA": 5, "TestE2EB": 5, "TestE2EC": 5, "TestE2ED": 5}
	first, err := plan(tests, timings, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, _ := plan(tests, timings, 3)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("plan changed between identical calls:\n%v\n%v", first, again)
		}
	}
}

func TestPlanRefusesMoreShardsThanTests(t *testing.T) {
	if _, err := plan([]string{"TestE2EA"}, nil, 2); err == nil {
		t.Fatal("2 shards for 1 test: want an error, since one shard would run nothing")
	}
	if _, err := plan([]string{"TestE2EA"}, nil, 0); err == nil {
		t.Fatal("0 shards: want an error")
	}
}

func TestSelectTests(t *testing.T) {
	names := []string{"TestE2EB", "TestE2ENightlySlow", "TestE2EA", "TestE2EA"}
	pr, err := selectTests(names, selectPR)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"TestE2EA", "TestE2EB"}; !reflect.DeepEqual(pr, want) {
		t.Errorf("pr = %v, want %v", pr, want)
	}
	all, err := selectTests(names, selectAll)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"TestE2EA", "TestE2EB", "TestE2ENightlySlow"}; !reflect.DeepEqual(all, want) {
		t.Errorf("all = %v, want %v", all, want)
	}
	if _, err := selectTests([]string{"TestE2EA", "TestUnit"}, selectAll); err == nil {
		t.Error("a non-TestE2E name must be refused: it means the list was wider than the e2e tests")
	}
	if _, err := selectTests([]string{"TestE2ENightlyOnly"}, selectPR); err == nil {
		t.Error("an empty selection must be refused")
	}
	if _, err := selectTests(names, "everything"); err == nil {
		t.Error("an unknown -select must be refused")
	}
}

func TestRunPlanWritesTheShardFiles(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(dir, "list.txt")
	timings := filepath.Join(dir, "timings.json")
	summary := filepath.Join(dir, "summary.md")
	out := filepath.Join(dir, "plan")
	if err := os.WriteFile(list, []byte("TestE2EA\nTestE2EB\nTestE2EC\nTestE2ENightlyD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(timings, []byte(`{"TestE2EA": 10, "TestE2EB": 20, "TestE2EC": 30}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"plan", "-list", list, "-timings", timings, "-shards", "2", "-select", "pr", "-out", out, "-summary", summary}, &stdout); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		s, err := readFileString(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := read("selected-tests.txt"); got != "TestE2EA\nTestE2EB\nTestE2EC\n" {
		t.Errorf("selected-tests.txt = %q", got)
	}
	// C(30)→0; B(20)→1; A(10)→1.
	if got := read("shard-0.txt"); got != "TestE2EC\n" {
		t.Errorf("shard-0.txt = %q", got)
	}
	if got := read("shard-1.txt"); got != "TestE2EA\nTestE2EB\n" {
		t.Errorf("shard-1.txt = %q", got)
	}
	if s, _ := readFileString(summary); s == "" || s != stdout.String() {
		t.Errorf("-summary file does not hold the printed summary:\nfile: %q\nstdout: %q", s, stdout.String())
	}
}

func maxLoad(p shardPlan) int {
	m := 0
	for _, s := range p.shards {
		if s.load > m {
			m = s.load
		}
	}
	return m
}

func maxInts(xs []int) int {
	m := 0
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}
