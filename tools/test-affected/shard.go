package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Sharding the Go test lane across CI runners.
//
// PR CI's Go tests are CPU-bound on a 4-vCPU runner: measured, `-p 4` and
// `-p 8` take the same ~540s, because compile and test bodies already fill
// every core. More cores is the only lever, so the packages are split across
// several runners (the backend matrix in .github/workflows/pr-ci.yml).
//
// Two things decide a shard's wall time, and both are handled here:
//
//   - Balance. Splitting the package list round-robin put every DB-heavy
//     package in one shard (179s vs 527s, measured). Packages are instead
//     assigned longest-first to the least-loaded shard (LPT), by their
//     measured duration in go-test-weights.tsv.
//   - Order. `go test` starts packages in command-line order. Alphabetically,
//     internal/workflow/runtime/activities/handlers (~100s) started 440s into a
//     540s run and was the whole tail. Each shard lists its packages heaviest
//     first, so the long poles start as soon as they are compiled.
//
// Correctness does not depend on the weights. Every package goes to exactly
// one shard whatever the weights say (TestShardPartitionsEveryPackage); a
// stale or missing weight only costs balance. A package with no weight gets
// defaultShardWeight.

// defaultShardWeight is the assumed duration, in seconds, of a package that
// go-test-weights.tsv does not name: most packages finish in well under a
// second, so a new one is assumed to be ordinary.
const defaultShardWeight = 1.0

// perPackageCost is added to every package's weight when balancing: the
// compile, vet and test-binary link a package costs before any test runs,
// which go-test-weights.tsv (test time only) cannot see. Without it the shard
// that collects the sub-second packages gets twice as many binaries to link.
const perPackageCost = 2.0

// weightsFile is the checked-in measurement shard reads by default, relative
// to the repository root.
const weightsFile = "tools/test-affected/go-test-weights.tsv"

// shardPackages returns the packages shard index (1-based) of total runs,
// heaviest first. pkgs is every package in the lane; weights maps a package
// to its measured seconds.
func shardPackages(pkgs []string, weights map[string]float64, index, total int) ([]string, error) {
	if total < 1 || index < 1 || index > total {
		return nil, fmt.Errorf("shard %d/%d: want 1 <= index <= total", index, total)
	}
	type item struct {
		pkg string
		w   float64
	}
	seen := map[string]bool{}
	items := make([]item, 0, len(pkgs))
	for _, p := range pkgs {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		w, ok := weights[p]
		if !ok {
			w = defaultShardWeight
		}
		items = append(items, item{p, w + perPackageCost})
	}
	// Heaviest first; the package path breaks ties so every runner, given the
	// same input, computes the same assignment.
	sort.Slice(items, func(i, j int) bool {
		if items[i].w != items[j].w {
			return items[i].w > items[j].w
		}
		return items[i].pkg < items[j].pkg
	})
	load := make([]float64, total)
	var out []string
	for _, it := range items {
		best := 0
		for s := 1; s < total; s++ {
			if load[s] < load[best] {
				best = s
			}
		}
		load[best] += it.w
		if best == index-1 {
			out = append(out, it.pkg)
		}
	}
	return out, nil
}

// readWeights parses go-test-weights.tsv: "<seconds>\t<package>" per line,
// '#' comments and blank lines ignored.
func readWeights(r io.Reader) (map[string]float64, error) {
	weights := map[string]float64{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		secs, pkg, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("line %d: want <seconds>\\t<package>, got %q", n, line)
		}
		w, err := strconv.ParseFloat(strings.TrimSpace(secs), 64)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		weights[strings.TrimSpace(pkg)] = w
	}
	return weights, sc.Err()
}

// goTestResultLine matches a package result line of `go test` output — "ok",
// "FAIL" or "---" for no test files is not a result — wherever it appears in
// the line, so a GitHub Actions log (prefixed with job, step and timestamp)
// parses as well as raw output. A "(cached)" result has no duration and is
// skipped: it says nothing about how long the package takes.
var goTestResultLine = regexp.MustCompile(`(?:^|\s)(?:ok|FAIL)\s+(\S+)\s+([0-9]+(?:\.[0-9]+)?)s(?:\s|$)`)

// measureWeights reads `go test` output (one or several logs concatenated)
// and returns each package's duration. A package seen more than once keeps
// its largest duration: weights should describe a slow run, not a lucky one.
func measureWeights(r io.Reader) (map[string]float64, error) {
	weights := map[string]float64{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		m := goTestResultLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		w, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		if w > weights[m[1]] {
			weights[m[1]] = w
		}
	}
	return weights, sc.Err()
}

// writeWeights writes weights in the go-test-weights.tsv format, heaviest
// first.
func writeWeights(w io.Writer, weights map[string]float64) error {
	pkgs := make([]string, 0, len(weights))
	for p := range weights {
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool {
		if weights[pkgs[i]] != weights[pkgs[j]] {
			return weights[pkgs[i]] > weights[pkgs[j]]
		}
		return pkgs[i] < pkgs[j]
	})
	if _, err := fmt.Fprint(w, "# Seconds each Go package's tests take under `go test -short` in PR CI.\n"+
		"# Read by `go run ./tools/test-affected shard` to balance the CI shards; a stale\n"+
		"# or missing entry only costs balance, never coverage. Regenerate from a CI log:\n"+
		"#   gh run view <run-id> --log | go run ./tools/test-affected weights > "+weightsFile+"\n"); err != nil {
		return err
	}
	for _, p := range pkgs {
		if _, err := fmt.Fprintf(w, "%.1f\t%s\n", weights[p], p); err != nil {
			return err
		}
	}
	return nil
}

// runShard implements `test-affected shard -index K -total N`: it reads the
// lane's packages from stdin (one per line, e.g. from `go list ./...`) and
// prints shard K's, heaviest first.
func runShard(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newFlagSet("shard")
	index := fs.Int("index", 0, "this shard, 1-based")
	total := fs.Int("total", 0, "number of shards")
	weightsPath := fs.String("weights", weightsFile, "go-test-weights.tsv to balance by")
	if err := fs.Parse(args); err != nil {
		return err
	}
	weights := map[string]float64{}
	if f, err := os.Open(*weightsPath); err == nil {
		weights, err = readWeights(f)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", *weightsPath, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var pkgs []string
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		if p := strings.TrimSpace(sc.Text()); p != "" {
			pkgs = append(pkgs, p)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(pkgs) == 0 {
		return fmt.Errorf("no packages on stdin")
	}
	out, err := shardPackages(pkgs, weights, *index, *total)
	if err != nil {
		return err
	}
	for _, p := range out {
		if _, err := fmt.Fprintln(stdout, p); err != nil {
			return err
		}
	}
	return nil
}

// runWeights implements `test-affected weights`: `go test` output on stdin,
// go-test-weights.tsv on stdout.
func runWeights(stdin io.Reader, stdout io.Writer) error {
	weights, err := measureWeights(stdin)
	if err != nil {
		return err
	}
	if len(weights) == 0 {
		return fmt.Errorf("no `ok <package> <duration>` lines on stdin")
	}
	return writeWeights(stdout, weights)
}
