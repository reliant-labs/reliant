package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type gateBudgets struct {
	Wall  int            `json:"wall_seconds"`
	Steps map[string]int `json:"steps"`
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// tscError is one TypeScript compiler diagnostic: "src/a.ts(12,3): error
// TS2322: ...". tsc prints nothing the test-failure patterns match, so a red
// typecheck phase used to name no failure at all (reliant bug report 7e9be182).
var tscError = regexp.MustCompile(`^\S+\(\d+,\d+\): error TS\d+: `)

// failedLogTail is how many trailing lines of a failed phase's log the report
// prints when that log names no failure the patterns recognise, so the job log
// and step summary always say why the phase failed: the full log stays on the
// runner, where nobody can read it once the job ends.
const failedLogTail = 40

// failures are extracted from the entire log, never a tail. Go's event filter
// attaches package identity at the fail event, before parallel logs interleave.
func gateFailures(text string) []string {
	out := map[string]bool{}
	for _, line := range strings.Split(ansiEscape.ReplaceAllString(text, ""), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "FAIL ") {
			line = "FAIL " + strings.TrimSpace(strings.TrimPrefix(line, "FAIL"))
		}
		if strings.HasPrefix(line, "--- FAIL:") && strings.Contains(line, "(package ") || strings.HasPrefix(line, "FAIL ") && strings.Contains(line, "> ") ||
			tscError.MatchString(line) {
			out[line] = true
		}
	}
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// gate-report emits the FIRST lines of summary.md: named test failures first,
// then gate-health warnings. A timing regression is not a test/PR failure.
func runGateReport(args []string, out, errOut io.Writer) int {
	fs := flag.NewFlagSet("gate-report", flag.ContinueOnError)
	fs.SetOutput(errOut)
	dir := fs.String("dir", "", "gate log directory")
	budgets := fs.String("budgets", "scripts/merge-gate-budgets.json", "budget contract")
	wall := fs.Int("wall", 0, "in-slot wall seconds")
	memPeak := fs.Int64("mem-peak", 0, "the gate cgroup's memory.peak in bytes (0: unknown)")
	memLimit := fs.Int64("mem-limit", 0, "the gate's memory limit in bytes (0: unknown)")
	if fs.Parse(args) != nil {
		return 2
	}
	b, err := os.ReadFile(*budgets)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	var limits gateBudgets
	if json.Unmarshal(b, &limits) != nil || limits.Wall <= 0 {
		fmt.Fprintln(errOut, "invalid gate budgets")
		return 2
	}
	logs, _ := filepath.Glob(filepath.Join(*dir, "*.log"))
	var text strings.Builder
	for _, p := range logs {
		b, err := os.ReadFile(p)
		if err == nil {
			text.Write(b)
			text.WriteByte('\n')
		}
	}
	failures := gateFailures(text.String())
	for _, f := range failures {
		fmt.Fprintln(out, f)
	}
	if len(failures) > 0 {
		fmt.Fprintln(out)
	}
	if *wall > limits.Wall {
		fmt.Fprintf(out, "!! GATE-HEALTH: wall %ds exceeds %ds; five-minute target NOT MET. Gate infrastructure/tiering regression, NOT PR blame.\n", *wall, limits.Wall)
	}
	rows, _ := filepath.Glob(filepath.Join(*dir, "*.row"))
	var failed []string
	unnamed := map[string]bool{}
	for _, p := range rows {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var name string
		var rc, seconds, start int
		if _, err = fmt.Sscanf(string(b), "%s %d %d %d", &name, &rc, &seconds, &start); err != nil {
			continue
		}
		limit, ok := limits.Steps[name]
		if !ok {
			fmt.Fprintf(out, "!! GATE-HEALTH: unbudgeted step %s (not PR blame).\n", name)
		} else if seconds > limit {
			fmt.Fprintf(out, "!! GATE-HEALTH: %s %ds exceeds %ds (not PR blame).\n", name, seconds, limit)
		}
		if rc != 0 {
			logPath := filepath.Join(*dir, name+".log")
			fmt.Fprintf(out, "failed phase: %s (rc %d); complete log: %s\n", name, rc, logPath)
			failed = append(failed, name)
			if tail := unnamedFailureTail(logPath); tail != "" {
				unnamed[name] = true
				fmt.Fprintf(out, "last lines of %s.log (it names no failure):\n%s", name, tail)
			}
		}
	}
	for _, m := range oomMarkers(len(failures) > 0, failed, unnamed, *memPeak, *memLimit, readOOMKills(*dir, failed)) {
		fmt.Fprintln(out, m)
	}
	return 0
}

// oomPeakPercent is how close to the memory limit the gate's peak must come
// for a failed phase that names no failure to count as killed for memory.
const oomPeakPercent = 95

// oomMarkers returns one "!! GATE-INFRA: oom <phase> (...)" line per failed
// phase the runner's memory killed rather than the change: every failed phase
// names no failure, and either the gate's cgroup peak reached oomPeakPercent
// of its limit or the kernel's oom_kill count rose while that phase ran.
// control-plane's pr-fix reads the marker from the job log and spends the one
// automatic re-run per head on it instead of blaming the PR (control-plane
// #2862), so it is all or nothing: a named failure anywhere, or a failed
// phase that does not qualify, prints no marker at all. peak and limit are
// bytes, 0 when unknown; oomKills is per phase.
func oomMarkers(named bool, failed []string, unnamed map[string]bool, peak, limit int64, oomKills map[string]int64) []string {
	if named || len(failed) == 0 {
		return nil
	}
	nearLimit := peak > 0 && limit > 0 && peak*100 >= limit*oomPeakPercent
	var out []string
	for _, name := range failed {
		if !unnamed[name] {
			return nil
		}
		var why []string
		if nearLimit {
			why = append(why, fmt.Sprintf("memory peak %.1f GiB of %.1f GiB", float64(peak)/(1<<30), float64(limit)/(1<<30)))
		}
		if k := oomKills[name]; k > 0 {
			why = append(why, fmt.Sprintf("%d oom_kill while it ran", k))
		}
		if len(why) == 0 {
			return nil
		}
		out = append(out, fmt.Sprintf("!! GATE-INFRA: oom %s (%s; its log names no failure): the runner ran out of memory, NOT PR blame.", name, strings.Join(why, ", ")))
	}
	return out
}

// readOOMKills reads <dir>/<phase>.oom, the rise in the cgroup's
// memory.events oom_kill count across that phase (merge-gate.sh's phase()),
// for each named phase. A missing or unreadable file is no kill.
func readOOMKills(dir string, phases []string) map[string]int64 {
	kills := map[string]int64{}
	for _, name := range phases {
		b, err := os.ReadFile(filepath.Join(dir, name+".oom"))
		if err != nil {
			continue
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && n > 0 {
			kills[name] = n
		}
	}
	return kills
}

// unnamedFailureTail is the last failedLogTail non-empty lines of a failed
// phase's log, indented, when that log names no failure gateFailures
// recognises; "" when it does (those lines already lead the report) or when
// the log cannot be read.
func unnamedFailureTail(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(gateFailures(string(b))) > 0 {
		return ""
	}
	var lines []string
	for _, line := range strings.Split(ansiEscape.ReplaceAllString(string(b), ""), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > failedLogTail {
		lines = lines[len(lines)-failedLogTail:]
	}
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString("    " + line + "\n")
	}
	return sb.String()
}
