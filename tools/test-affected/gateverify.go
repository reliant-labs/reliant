// Copyright (c) 2025 Reliant Labs
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// runGateVerify is `test-affected gate-verify`: the merge gate's closing
// check. scripts/merge-gate.sh runs every phase the plan selected and leaves
// one "<name> <rc> <secs> <start>" row per phase that finished. This judges
// whether every phase the plan OBLIGES ran to completion and passed. A phase
// with no row (queued for a slot and cut off, cancelled, timed out, its tool
// missing) is a failure naming it, never a pass: reliant#1393 merged with its
// docs-generate lane still queued. The verdict (one line, "OK" or "FAILED:
// ...") is also written to <dir>/verdict for the merge queue to quote.
func runGateVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gate-verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	planPath := fs.String("plan", "", "plan.json written by `test-affected plan -json`")
	dir := fs.String("dir", "", "the gate's log directory (<phase>.row files)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *planPath == "" || *dir == "" {
		fmt.Fprintln(stderr, "gate-verify: -plan and -dir are required")
		return 2
	}
	problems := verifyGate(*planPath, *dir)
	verdict := "OK"
	if len(problems) > 0 {
		verdict = "FAILED: " + strings.Join(problems, "; ")
	}
	_ = os.WriteFile(filepath.Join(*dir, "verdict"), []byte(verdict+"\n"), 0o644)
	if len(problems) == 0 {
		fmt.Fprintln(stdout, "gate-verify: every phase the plan selected completed and passed")
		return 0
	}
	for _, p := range problems {
		fmt.Fprintln(stderr, "gate-verify: "+p)
	}
	fmt.Fprintln(stderr, "gate-verify: a phase that never completed is a failure, not a pass")
	return 1
}

// verifyGate returns one problem per phase that is obliged but did not
// complete green, sorted by phase name.
func verifyGate(planPath, dir string) []string {
	b, err := os.ReadFile(planPath)
	if err != nil {
		return []string{"plan unreadable (" + err.Error() + "), so what was selected is unknown"}
	}
	var pl Plan
	if err := json.Unmarshal(b, &pl); err != nil {
		return []string{"plan unreadable (" + err.Error() + "), so what was selected is unknown"}
	}

	obliged := map[string]string{
		"build": "every gate builds",
		"test":  "every gate runs the affected tests",
	}
	if pl.ForgeGen.Full {
		obliged["generate"] = "the plan selected the forge-generate lane"
	}
	if pl.DocGen.Full {
		obliged["docs"] = "the plan selected the docs-generate lane"
	}
	if pl.MigOrder.Full {
		obliged["migorder"] = "the plan selected the migration-order lane"
	}
	var migration, pin, subdir, web bool
	for _, f := range pl.Files {
		p := f.Path
		switch {
		case strings.HasPrefix(p, "db/migrations/"):
			migration = true
		case p == "forge.yaml":
			migration, pin = true, true
		case p == "go.mod" || p == "go.sum":
			pin = true
		}
		if strings.HasPrefix(p, "frontends/web/") {
			web = true
		}
		if !f.Deleted && strings.Contains(p, "/") {
			subdir = true
		}
	}
	if pl.Web.Full || len(pl.Web.Tests) > 0 || web {
		obliged["typecheck"] = "a web lane or frontends/web/ change needs the type-check"
	}
	if migration {
		obliged["migsafety"] = "a migration or forge.yaml changed"
	}
	if pin || subdir {
		obliged["lint"] = "the diff touches linted code"
	}
	// Phases the script itself decided to run (it appends each to <dir>/expected
	// before launching it), so a phase it chose that left no row also fails.
	if eb, err := os.ReadFile(filepath.Join(dir, "expected")); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(eb)))
		for sc.Scan() {
			if n := strings.TrimSpace(sc.Text()); n != "" {
				if _, ok := obliged[n]; !ok {
					obliged[n] = "the gate started it"
				}
			}
		}
	}

	rows := map[string]int{} // phase -> rc; absent = never completed
	rowFiles, _ := filepath.Glob(filepath.Join(dir, "*.row"))
	for _, rf := range rowFiles {
		name := strings.TrimSuffix(filepath.Base(rf), ".row")
		rb, err := os.ReadFile(rf)
		if err != nil {
			continue
		}
		fields := strings.Fields(string(rb))
		if len(fields) < 4 {
			continue
		}
		rc, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		rows[name] = rc
	}

	var problems []string
	names := make([]string, 0, len(obliged))
	for n := range obliged {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		rc, ok := rows[n]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s never completed (no result row; %s)", n, obliged[n]))
		case rc != 0:
			problems = append(problems, fmt.Sprintf("%s failed (rc %d)", n, rc))
		}
	}
	// Any other phase that ran (npm-* ...) must be green too.
	var extra []string
	for n, rc := range rows {
		if _, ok := obliged[n]; !ok && rc != 0 {
			extra = append(extra, fmt.Sprintf("%s failed (rc %d)", n, rc))
		}
	}
	sort.Strings(extra)
	return append(problems, extra...)
}
