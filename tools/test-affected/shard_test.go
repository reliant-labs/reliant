package main

import (
	"bytes"
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
		for total := 1; total <= 5; total++ {
			t.Run(fmt.Sprintf("%s/%d", name, total), func(t *testing.T) {
				seen := map[string]int{}
				for index := 1; index <= total; index++ {
					got, err := shardPackages(pkgs, weights, index, total)
					if err != nil {
						t.Fatal(err)
					}
					for _, p := range got {
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

func TestShardBalancesAndOrdersHeaviestFirst(t *testing.T) {
	pkgs := []string{"a", "b", "c", "d", "e", "f"}
	weights := map[string]float64{"a": 100, "b": 90, "c": 50, "d": 40, "e": 5, "f": 1}
	load := func(ps []string) (sum float64) {
		for _, p := range ps {
			sum += weights[p]
		}
		return sum
	}
	s1, _ := shardPackages(pkgs, weights, 1, 2)
	s2, _ := shardPackages(pkgs, weights, 2, 2)
	if l1, l2 := load(s1), load(s2); l1 > 1.2*l2 || l2 > 1.2*l1 {
		t.Errorf("unbalanced: %v=%.0f vs %v=%.0f", s1, l1, s2, l2)
	}
	for _, s := range [][]string{s1, s2} {
		if !sort.SliceIsSorted(s, func(i, j int) bool { return weights[s[i]] > weights[s[j]] }) {
			t.Errorf("shard %v is not heaviest first", s)
		}
	}
}

func TestShardRejectsBadIndex(t *testing.T) {
	for _, c := range [][2]int{{0, 2}, {3, 2}, {1, 0}} {
		if _, err := shardPackages([]string{"a"}, nil, c[0], c[1]); err == nil {
			t.Errorf("shard %d/%d: want an error", c[0], c[1])
		}
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
