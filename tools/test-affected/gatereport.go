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
			if tail := unnamedFailureTail(logPath); tail != "" {
				fmt.Fprintf(out, "last lines of %s.log (it names no failure):\n%s", name, tail)
			}
		}
	}
	return 0
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
