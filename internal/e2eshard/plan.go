package main

import (
	"fmt"
	"sort"
	"strings"
)

const (
	e2ePrefix     = "TestE2E"
	nightlyPrefix = "TestE2ENightly"

	selectPR  = "pr"
	selectAll = "all"
)

// selectTests returns the sorted, de-duplicated tests a run of kind sel
// executes. Every name must carry the e2e prefix: anything else means the
// list was produced by a wider -test.list than the shards may run, and
// planning it would put internal/cli's unit tests back into the shards.
func selectTests(names []string, sel string) ([]string, error) {
	if sel != selectPR && sel != selectAll {
		return nil, fmt.Errorf("-select %q: want %q or %q", sel, selectPR, selectAll)
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !strings.HasPrefix(n, e2ePrefix) {
			return nil, fmt.Errorf("%q is not an e2e test: list the binary with -test.list '^%s'", n, e2ePrefix)
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		if sel == selectPR && strings.HasPrefix(n, nightlyPrefix) {
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no tests selected (-select %s, %d listed)", sel, len(names))
	}
	sort.Strings(out)
	return out, nil
}

// shardPlan is one run's assignment of tests to shards.
type shardPlan struct {
	shards []shard
	// estimated names the tests planned at the fallback duration because
	// the timings file has no entry for them.
	estimated []string
	fallback  int
}

type shard struct {
	tests []string // sorted
	load  int      // expected seconds: the sum of the tests' durations
}

// plan splits tests into n shards, longest first, each test onto the shard
// with the least expected load so far (ties to the lower index). Every test
// lands in exactly one shard, and with at least n tests no shard is empty:
// the first n assignments go to the n still-empty shards.
//
// A test without a recorded duration is planned at the median of the
// durations recorded for the selected tests, so a new test is neither
// stacked onto one shard as free nor treated as the slowest.
func plan(tests []string, timings map[string]int, n int) (shardPlan, error) {
	if n < 1 {
		return shardPlan{}, fmt.Errorf("-shards %d: need at least one shard", n)
	}
	if len(tests) < n {
		return shardPlan{}, fmt.Errorf("%d tests cannot fill %d shards: a shard with nothing to run would pass vacuously", len(tests), n)
	}
	p := shardPlan{shards: make([]shard, n), fallback: medianKnown(tests, timings)}
	type job struct {
		name string
		secs int
	}
	jobs := make([]job, 0, len(tests))
	for _, t := range tests {
		secs, ok := timings[t]
		if !ok || secs <= 0 {
			secs = p.fallback
			p.estimated = append(p.estimated, t)
		}
		jobs = append(jobs, job{t, secs})
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].secs != jobs[j].secs {
			return jobs[i].secs > jobs[j].secs
		}
		return jobs[i].name < jobs[j].name
	})
	for _, j := range jobs {
		best := 0
		for i := 1; i < n; i++ {
			if p.shards[i].load < p.shards[best].load {
				best = i
			}
		}
		p.shards[best].tests = append(p.shards[best].tests, j.name)
		p.shards[best].load += j.secs
	}
	for i := range p.shards {
		sort.Strings(p.shards[i].tests)
	}
	sort.Strings(p.estimated)
	return p, nil
}

// medianKnown is the median recorded duration among tests, or 60 seconds
// when none of them has one (a first run, or a wiped timings file).
func medianKnown(tests []string, timings map[string]int) int {
	var known []int
	for _, t := range tests {
		if s, ok := timings[t]; ok && s > 0 {
			known = append(known, s)
		}
	}
	if len(known) == 0 {
		return 60
	}
	sort.Ints(known)
	return known[len(known)/2]
}

func (p shardPlan) markdown(sel string, listed int) string {
	var b strings.Builder
	total := 0
	for _, s := range p.shards {
		total += s.load
	}
	selected := 0
	for _, s := range p.shards {
		selected += len(s.tests)
	}
	fmt.Fprintf(&b, "### e2e shard plan (-select %s)\n\n", sel)
	fmt.Fprintf(&b, "%d of %d listed `%s*` tests selected, %d shards, %ds of recorded test time.\n\n", selected, listed, e2ePrefix, len(p.shards), total)
	b.WriteString("| shard | tests | expected test-seconds |\n| --- | --- | --- |\n")
	for i, s := range p.shards {
		fmt.Fprintf(&b, "| %d | %d | %d |\n", i, len(s.tests), s.load)
	}
	b.WriteString("\n")
	if len(p.estimated) > 0 {
		fmt.Fprintf(&b, "%d test(s) have no recorded timing and were planned at the median, %ds. "+
			"`scripts/e2e-timings.sh` records them from a CI run:\n\n", len(p.estimated), p.fallback)
		for _, t := range p.estimated {
			fmt.Fprintf(&b, "- `%s`\n", t)
		}
		b.WriteString("\n")
	}
	return b.String()
}
