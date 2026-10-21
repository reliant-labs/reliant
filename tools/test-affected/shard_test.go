package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// The property CI's correctness rests on: whatever the weights, the shards
// together run every package exactly once. A package in no shard is a test
// that silently never runs on a PR.
func TestShardPartitionsEveryPackage(t *testing.T) {
	var pkgs []string
	for i := range 57 {
		pkgs = append(pkgs, fmt.Sprintf("example.com/m/p%02d", i))
	}
	weightSets := map[string]map[string]float64{
		"none":   {},
		"skewed": {"example.com/m/p03": 120, "example.com/m/p40": 95, "example.com/m/p41": 60},
		"uniform": func() map[string]float64 {
			w := map[string]float64{}
			for _, p := range pkgs {
				w[p] = 2
			}
			return w
		}(),
		"unknown": {"example.com/m/gone": 500}, // a weight for a package that no longer exists
	}
	for name, weights := range weightSets {
		for _, target := range []float64{1, 10, 60, 360, 1e9} {
			for limit := 1; limit <= 5; limit++ {
				t.Run(fmt.Sprintf("%s/target=%v/max=%d", name, target, limit), func(t *testing.T) {
					shards := planShards(pkgs, weights, target, limit)
					if len(shards) < 1 || len(shards) > limit {
						t.Fatalf("%d shards, want 1..%d", len(shards), limit)
					}
					seen := map[string]int{}
					for i, s := range shards {
						if len(s) == 0 {
							t.Errorf("shard %d/%d is empty: a runner launched to run nothing", i+1, len(shards))
						}
						for _, p := range s {
							seen[p]++
						}
					}
					for _, p := range pkgs {
						if seen[p] != 1 {
							t.Errorf("%s is in %d shards, want exactly 1", p, seen[p])
						}
					}
					if len(seen) != len(pkgs) {
						t.Errorf("shards hold %d distinct packages, want %d", len(seen), len(pkgs))
					}
				})
			}
		}
	}
}

// A small selection runs on one runner; only a large one pays for more. Each
// extra shard is a runner's setup and cache restore, and a slot in the org's
// concurrent-job limit.
func TestShardCountFollowsTheSelectionsWeight(t *testing.T) {
	weights := map[string]float64{"heavy": 300, "mid": 150}
	cases := []struct {
		name string
		pkgs []string
		want int
	}{
		{"nothing selected", nil, 0},
		{"one light package", []string{"leaf"}, 1},
		{"under one target", []string{"mid", "leaf"}, 1},                // 152+3 <= 360
		{"just over one target", []string{"heavy", "mid"}, 2},           // 302+152 > 360
		{"more weight than packages", []string{"heavy", "mid", "x"}, 2}, // 457 -> 2
	}
	for _, c := range cases {
		if got := len(planShards(c.pkgs, weights, 360, 4)); got != c.want {
			t.Errorf("%s: %d shards, want %d", c.name, got, c.want)
		}
	}
	var many []string
	for i := range 400 {
		many = append(many, fmt.Sprintf("p%03d", i))
	}
	if got := len(planShards(many, nil, 360, 4)); got != 4 {
		t.Errorf("the whole module: %d shards, want the cap of 4", got)
	}
	if got := shardCount(1e6, 1, 4, 2); got != 2 {
		t.Errorf("shardCount with 2 packages = %d, want 2: a shard with no package is a wasted runner", got)
	}
}

func TestShardBalancesAndOrdersHeaviestFirst(t *testing.T) {
	pkgs := []string{"a", "b", "c", "d", "e", "f"}
	weights := map[string]float64{"a": 100, "b": 90, "c": 50, "d": 40, "e": 5, "f": 1}
	load := func(ps []string) (sum float64) {
		for _, p := range ps {
			sum += weights[p]
		}
		return sum
	}
	shards := planShards(pkgs, weights, 150, 2)
	if len(shards) != 2 {
		t.Fatalf("%d shards, want 2", len(shards))
	}
	s1, s2 := shards[0], shards[1]
	if l1, l2 := load(s1), load(s2); l1 > 1.2*l2 || l2 > 1.2*l1 {
		t.Errorf("unbalanced: %v=%.0f vs %v=%.0f", s1, l1, s2, l2)
	}
	for _, s := range shards {
		if !sort.SliceIsSorted(s, func(i, j int) bool { return weights[s[i]] > weights[s[j]] }) {
			t.Errorf("shard %v is not heaviest first", s)
		}
	}
}

// The workflow expands this output as the backend matrix verbatim.
func TestShardPrintsTheBackendMatrix(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("example.com/m/a\nexample.com/m/b\n\n")
	if err := runShard([]string{"-weights", "/nonexistent", "-target", "1", "-max", "4"}, in, &out); err != nil {
		t.Fatal(err)
	}
	var m shardMatrix
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %q: %v", out.String(), err)
	}
	if len(m.Include) != 2 || m.Include[0].Shard != 1 || m.Include[1].ShardTotal != 2 {
		t.Fatalf("matrix = %+v, want shards 1/2 and 2/2", m)
	}
	if got := m.Include[0].Packages + " " + m.Include[1].Packages; !strings.Contains(got, "example.com/m/a") || !strings.Contains(got, "example.com/m/b") {
		t.Errorf("packages %q lost one", got)
	}

	out.Reset()
	if err := runShard(nil, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("an empty selection printed %q, want nothing (no Go lane)", out.String())
	}
}

// The weights are measured from real CI logs, whose lines carry a job, step
// and timestamp prefix.
func TestMeasureWeightsFromAnActionsLog(t *testing.T) {
	log := strings.Join([]string{
		"Backend (Go, short)\tGo tests\t2026-10-15T10:30:35.5Z ok  \tgithub.com/x/r/internal/db\t116.013s",
		"Backend (Go, short)\tGo tests\t2026-10-15T10:30:35.5Z ok  \tgithub.com/x/r/internal/fast\t0.077s",
		"ok  \tgithub.com/x/r/internal/cached\t(cached)",
		"FAIL\tgithub.com/x/r/internal/broken\t3.5s",
		"--- FAIL: TestSomething (0.94s)",
		"?   \tgithub.com/x/r/cmd/none\t[no test files]",
		"ok  \tgithub.com/x/r/internal/db\t90.000s", // a second log: the slower run wins
	}, "\n")
	got, err := measureWeights(strings.NewReader(log))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"github.com/x/r/internal/db":     116.013,
		"github.com/x/r/internal/fast":   0.077,
		"github.com/x/r/internal/broken": 3.5,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s = %v, want %v", p, got[p], w)
		}
	}

	var buf bytes.Buffer
	if err := writeWeights(&buf, got); err != nil {
		t.Fatal(err)
	}
	back, err := readWeights(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if back["github.com/x/r/internal/db"] != 116.0 || len(back) != 3 {
		t.Errorf("round trip: got %v", back)
	}
}

// The checked-in weights must parse, or every CI shard falls back to
// unbalanced.
func TestCheckedInWeightsParse(t *testing.T) {
	f, err := os.Open("go-test-weights.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w, err := readWeights(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(w) < 50 {
		t.Errorf("go-test-weights.tsv names %d packages; regenerate it from a CI log", len(w))
	}
}
