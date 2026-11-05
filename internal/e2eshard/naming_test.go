package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// cliDir is the package e2e-suite.yml shards: the only one its shards run.
var cliDir = filepath.Join("..", "cli")

// TestE2ETestsAreNamedForTheirTag is the guard that makes `-test.list
// '^TestE2E'` select exactly the e2e-tagged tests, which is how the e2e
// shards pick their tests (see the package doc). Both directions matter:
//
//   - a test in an e2e-tagged file without the prefix is never selected, so
//     it never runs anywhere — the Test job does not compile the file and
//     the shards do not select the name;
//   - a TestE2E test in an untagged file is selected by the shards AND run
//     by the Test job, which is the duplication the prefix exists to end.
//
// It parses the files itself (scanTests), so the test cache keys on them:
// a cached PASS cannot outlive the edit that breaks the rule.
func TestE2ETestsAreNamedForTheirTag(t *testing.T) {
	tests, err := scanTests(cliDir)
	if err != nil {
		t.Fatal(err)
	}
	e2e := 0
	for _, tf := range tests {
		prefixed := strings.HasPrefix(tf.name, e2ePrefix)
		switch {
		case tf.e2eFile && !prefixed:
			t.Errorf("%s: %s is in an e2e-tagged file but is not named %s…: the e2e shards select by that prefix, "+
				"so it would run nowhere. Rename it %s%s.", tf.file, tf.name, e2ePrefix, e2ePrefix, strings.TrimPrefix(tf.name, "Test"))
		case !tf.e2eFile && prefixed:
			t.Errorf("%s: %s is named %s… but its file has no e2e build tag, so the Test job and the e2e shards "+
				"would both run it. Tag the file `//go:build e2e`, or rename the test.", tf.file, tf.name, e2ePrefix)
		}
		if tf.e2eFile {
			e2e++
		}
	}
	// A floor, so a parse that silently finds nothing (a moved package, a
	// changed build line) cannot pass this guard by inspecting nothing.
	if e2e < 50 {
		t.Fatalf("found %d e2e tests in %s; the suite has ~90 — has the package moved?", e2e, cliDir)
	}
}

// TestEveryE2ETestLandsInExactlyOneShard plans the real suite against the
// committed timings for every shard count the workflow could plausibly use,
// and checks the plan partitions the selection: each selected test in
// exactly one shard, nothing else in any shard, no shard empty. The ledger
// job re-checks the same property on every CI run against what the shards
// actually reported.
func TestEveryE2ETestLandsInExactlyOneShard(t *testing.T) {
	tests, err := scanTests(cliDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tf := range tests {
		if tf.e2eFile {
			names = append(names, tf.name)
		}
	}
	timings, err := readTimings(filepath.Join("..", "..", ".github", "e2e-timings.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, sel := range []string{selectPR, selectAll} {
		selected, err := selectTests(names, sel)
		if err != nil {
			t.Fatal(err)
		}
		for n := 1; n <= 8; n++ {
			p, err := plan(selected, timings, n)
			if err != nil {
				t.Fatalf("-select %s, %d shards: %v", sel, n, err)
			}
			assertPartition(t, selected, p)
		}
	}
}

// TestCommittedTimingsAreCurrent keeps .github/e2e-timings.json honest
// without making it a chore: a test with no entry is planned at the median
// (so a new test never needs one to merge), but an entry for a test that no
// longer exists is noise that `scripts/e2e-timings.sh` would drop, and an
// encoding other than encodeTimings' churns diffs.
func TestCommittedTimingsAreCurrent(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "e2e-timings.json")
	timings, err := readTimings(path)
	if err != nil {
		t.Fatal(err)
	}
	tests, err := scanTests(cliDir)
	if err != nil {
		t.Fatal(err)
	}
	exists := map[string]bool{}
	for _, tf := range tests {
		if tf.e2eFile {
			exists[tf.name] = true
		}
	}
	for name, secs := range timings {
		if !exists[name] {
			t.Errorf("%s: entry for %s, which is not an e2e test in %s — re-run scripts/e2e-timings.sh, or delete the line", path, name, cliDir)
		}
		if secs <= 0 {
			t.Errorf("%s: %s = %d; durations are whole seconds, at least 1", path, name, secs)
		}
	}
	raw, err := readFileString(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := encodeTimings(timings)
	if err != nil {
		t.Fatal(err)
	}
	if raw != string(want) {
		t.Errorf("%s is not in e2eshard's encoding (sorted, one entry per line, two-space indent); regenerate it with scripts/e2e-timings.sh", path)
	}
}
