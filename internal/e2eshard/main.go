// Command e2eshard plans and maintains the sharding of forge's `-tags e2e`
// suite (.github/workflows/e2e-suite.yml). It is CI plumbing, run with
// `go run ./internal/e2eshard`, and no part of the forge binary.
//
//	e2eshard list     [-dir internal/cli]
//	    The e2e tests, read from source: every top-level Test function in a
//	    file built only under the e2e tag. This is the set the naming guard
//	    (naming_test.go) holds to the TestE2E prefix.
//
//	e2eshard plan     -list FILE -timings FILE -shards N -select pr|all -out DIR [-summary FILE]
//	    Split the tests named in FILE (the e2e test binary's
//	    `-test.list '^TestE2E'` output) into N shards of roughly equal
//	    recorded duration. Writes DIR/selected-tests.txt and
//	    DIR/shard-<i>.txt.
//
//	e2eshard timings  [-merge FILE] [-dir internal/cli] SHARD.json...
//	    Fold `go test -json` streams from shard runs into a timings file on
//	    stdout: each test's median duration across the streams, over the
//	    entries of -merge for tests the streams did not run. With -dir,
//	    entries for tests that no longer exist are dropped.
//	    scripts/e2e-timings.sh drives this against a CI run.
//
// WHY A NAMING CONVENTION. The shards must run the e2e tests and nothing
// else: internal/cli's ~3,000 untagged unit tests are already run, under
// -race, by ci.yml's Test (internal/cli) job, and running them again in the
// shards cost each shard 50-200 test-seconds for no signal. A build tag
// cannot be selected at run time; a name can. So a test is an e2e test
// exactly when its name starts with TestE2E, and naming_test.go fails the
// moment a test in an e2e-tagged file is named otherwise, or a TestE2E test
// lives in an untagged file. The shards select `-test.list '^TestE2E'` from
// the compiled binary, which by that guard is precisely the tagged set.
//
// WHY RECORDED DURATIONS. The shards used to split the sorted test list
// round-robin (`NR % 4`), which balanced test COUNTS: one run's slowest shard
// carried 3,162 test-seconds while its fastest carried 1,544, and the lane
// waits for the slowest. The plan is longest-processing-time-first over the
// committed timings, so every shard's expected load is close to the mean. A
// test with no recorded timing yet (a new one) is planned at the median of
// the known ones; a stale timing only reorders work, it never drops a test.
//
// NIGHTLY TESTS. A test named TestE2ENightly* is part of the suite but not of
// a pull request's run: `-select pr` leaves it out and nightly.yml runs the
// whole suite with `-select all`. Move a test there only when it is slow and
// a regression in it is cheap to bisect a day late; say why in its comment.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "e2eshard:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: e2eshard list|plan|timings [flags]")
	}
	switch args[0] {
	case "list":
		return runList(args[1:], stdout)
	case "plan":
		return runPlan(args[1:], stdout)
	case "timings":
		return runTimings(args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q: want list, plan or timings", args[0])
	}
}

func runList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	dir := fs.String("dir", filepath.Join("internal", "cli"), "package directory to read")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tests, err := scanTests(*dir)
	if err != nil {
		return err
	}
	for _, t := range tests {
		if t.e2eFile {
			fmt.Fprintln(stdout, t.name)
		}
	}
	return nil
}

func runPlan(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	listPath := fs.String("list", "", "file of test names, one per line (the e2e binary's -test.list output)")
	timingsPath := fs.String("timings", "", "recorded durations (JSON object: test name -> seconds)")
	shards := fs.Int("shards", 0, "number of shards")
	sel := fs.String("select", selectPR, "which tests: pr (all but TestE2ENightly*) or all")
	outDir := fs.String("out", "", "directory for selected-tests.txt and shard-<i>.txt")
	summaryPath := fs.String("summary", "", "append a markdown summary to this file (e.g. $GITHUB_STEP_SUMMARY)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *listPath == "" || *timingsPath == "" || *outDir == "" {
		return fmt.Errorf("plan: -list, -timings and -out are required")
	}
	names, err := readLines(*listPath)
	if err != nil {
		return err
	}
	timings, err := readTimings(*timingsPath)
	if err != nil {
		return err
	}
	selected, err := selectTests(names, *sel)
	if err != nil {
		return err
	}
	p, err := plan(selected, timings, *shards)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	if err := writeLines(filepath.Join(*outDir, "selected-tests.txt"), selected); err != nil {
		return err
	}
	for i, s := range p.shards {
		if err := writeLines(filepath.Join(*outDir, fmt.Sprintf("shard-%d.txt", i)), s.tests); err != nil {
			return err
		}
	}
	summary := p.markdown(*sel, len(names))
	fmt.Fprint(stdout, summary)
	if *summaryPath != "" {
		f, err := os.OpenFile(*summaryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := f.WriteString(summary); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	}
	return nil
}

func runTimings(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("timings", flag.ContinueOnError)
	mergePath := fs.String("merge", "", "existing timings file; its entries stand for tests the streams did not run")
	dir := fs.String("dir", "", "package directory: drop entries for tests that no longer exist there")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("timings: name at least one `go test -json` file")
	}
	var streams []io.Reader
	for _, p := range fs.Args() {
		// A shard's stream is a few MB; reading it whole keeps no file open.
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		streams = append(streams, bytes.NewReader(b))
	}
	fresh, err := durationsFromStreams(streams...)
	if err != nil {
		return err
	}
	if len(fresh) == 0 {
		return fmt.Errorf("timings: the streams hold no passed or failed top-level test")
	}
	out := map[string]int{}
	if *mergePath != "" {
		old, err := readTimings(*mergePath)
		if err != nil {
			return err
		}
		for k, v := range old {
			out[k] = v
		}
	}
	for k, v := range fresh {
		out[k] = v
	}
	if *dir != "" {
		tests, err := scanTests(*dir)
		if err != nil {
			return err
		}
		exists := map[string]bool{}
		for _, t := range tests {
			if t.e2eFile {
				exists[t.name] = true
			}
		}
		for k := range out {
			if !exists[k] {
				delete(out, k)
			}
		}
	}
	b, err := encodeTimings(out)
	if err != nil {
		return err
	}
	_, err = stdout.Write(b)
	return err
}

func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

func writeLines(path string, lines []string) error {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return atomicfile.WriteFile(path, []byte(b.String()), 0o644)
}
