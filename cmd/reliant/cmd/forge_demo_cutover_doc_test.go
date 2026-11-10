// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	forgecli "github.com/reliant-labs/forge/cli"
)

// forgeDemoCutoverDoc is the runbook whose commands an operator pastes into
// prod: moving demo.reliantlabs.io between control-plane's prod and reliant's
// demo env, and back.
const forgeDemoCutoverDoc = "../../../dev-docs/forge-demo-cutover.md"

// envAssignment is a leading `NAME=value` on a shell line.
var envAssignment = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*=\S*$`)

// forgeDocCommand is one `forge …` line of a fenced bash block.
type forgeDocCommand struct {
	line int
	args []string // after `forge` / `reliant forge`
}

// forgeDocCommands is every `forge domain …` and `forge env status …` line in
// the doc's ```bash blocks, comments stripped.
func forgeDocCommands(t *testing.T, path string) []forgeDocCommand {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []forgeDocCommand
	inBash := false
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "```") {
			inBash = !inBash && strings.TrimPrefix(line, "```") == "bash"
			continue
		}
		if !inBash {
			continue
		}
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		for len(fields) > 0 && envAssignment.MatchString(fields[0]) {
			fields = fields[1:]
		}
		switch {
		case len(fields) > 1 && fields[0] == "reliant" && fields[1] == "forge":
			fields = fields[2:]
		case len(fields) > 0 && fields[0] == "forge":
			fields = fields[1:]
		default:
			continue
		}
		if len(fields) > 0 && (fields[0] == "domain" || (fields[0] == "env" && len(fields) > 1 && fields[1] == "status")) {
			out = append(out, forgeDocCommand{line: n, args: fields})
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every domain and env-status command the cutover runbook tells an operator to
// run parses against the forge command tree reliant embeds (the forge
// forge.yaml pins): the subcommand exists, every flag exists, the positional
// arguments fit, `--env` is given, and a bind names exactly one of --target
// and --redirect-to. Nothing runs: a renamed command or flag fails here, not
// in the middle of a prod domain move.
func TestForgeDemoCutoverDomainCommandsParse(t *testing.T) {
	path := filepath.FromSlash(forgeDemoCutoverDoc)
	cmds := forgeDocCommands(t, path)

	binds := map[string]bool{}
	for _, c := range cmds {
		root := forgecli.NewRootCmd()
		sub, rest, err := root.Find(c.args)
		if err != nil || sub == root {
			t.Errorf("%s:%d: `forge %s`: no such command (%v)", path, c.line, strings.Join(c.args, " "), err)
			continue
		}
		if err := sub.ParseFlags(rest); err != nil {
			t.Errorf("%s:%d: `forge %s`: %v", path, c.line, strings.Join(c.args, " "), err)
			continue
		}
		if err := sub.ValidateArgs(sub.Flags().Args()); err != nil {
			t.Errorf("%s:%d: `forge %s`: %v", path, c.line, strings.Join(c.args, " "), err)
			continue
		}
		if c.args[0] != "domain" {
			continue
		}
		env, _ := sub.Flags().GetString("env")
		if env == "" {
			t.Errorf("%s:%d: `forge %s` names no --env: forge refuses it", path, c.line, strings.Join(c.args, " "))
		}
		if sub.Name() == "bind" {
			target, _ := sub.Flags().GetString("target")
			redirect, _ := sub.Flags().GetString("redirect-to")
			if (target == "") == (redirect == "") {
				t.Errorf("%s:%d: `forge %s` must name exactly one of --target and --redirect-to", path, c.line, strings.Join(c.args, " "))
			}
			binds[env+"/"+target] = true
		}
	}
	// Not vacuous: the page carries the move into prod and the rollback to demo.
	for _, want := range []string{"prod/forge-demo", "demo/forge-demo"} {
		if !binds[want] {
			t.Errorf("%s: no `forge domain bind … --env %s --target %s` found; parsed %d commands",
				path, strings.Split(want, "/")[0], strings.Split(want, "/")[1], len(cmds))
		}
	}
}
