package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
)

// readTimings reads a timings file: a JSON object mapping a test name to its
// recorded duration in whole seconds.
func readTimings(path string) (map[string]int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// encodeTimings renders a timings file: one test per line in name order,
// two-space indent, trailing newline — the bytes prettier and the pre-commit
// JSON hooks leave alone, so a regenerated file diffs by the lines that
// changed.
func encodeTimings(t map[string]int) ([]byte, error) {
	names := make([]string, 0, len(t))
	for k := range t {
		names = append(names, k)
	}
	sort.Strings(names)
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, n := range names {
		key, err := json.Marshal(n)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "  %s: %d", key, t[n])
		if i < len(names)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}

// durationsFromStreams returns, for every top-level test that passed or
// failed in any of the `go test -json` streams, the median of its elapsed
// times in whole seconds (at least 1). Skips carry no duration worth
// planning on, and subtests are folded into their parent's elapsed time
// already. A line that is not JSON — a toolchain message that escaped
// test2json — is ignored, as the workflow's renderer ignores it.
func durationsFromStreams(streams ...io.Reader) (map[string]int, error) {
	samples := map[string][]float64{}
	for _, r := range streams {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for sc.Scan() {
			var ev struct {
				Action  string
				Test    string
				Elapsed float64
			}
			if json.Unmarshal(sc.Bytes(), &ev) != nil {
				continue
			}
			if ev.Test == "" || strings.Contains(ev.Test, "/") {
				continue
			}
			if ev.Action != "pass" && ev.Action != "fail" {
				continue
			}
			samples[ev.Test] = append(samples[ev.Test], ev.Elapsed)
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	out := make(map[string]int, len(samples))
	for name, s := range samples {
		sort.Float64s(s)
		m := s[len(s)/2]
		if len(s)%2 == 0 {
			m = (s[len(s)/2-1] + s[len(s)/2]) / 2
		}
		out[name] = int(math.Max(1, math.Round(m)))
	}
	return out, nil
}
