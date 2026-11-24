// Copyright (c) 2026 Reliant Labs

package lint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// hiddenFromNextMinor are the agent CLIs whose sign-in a `credentials: none`
// command can still read in this release and will not from the next minor
// (daemonpolicy's deprecated-readable tier), by binary, with where each keeps
// it.
var hiddenFromNextMinor = map[string]string{
	"gemini":       "~/.gemini",
	"cursor-agent": "~/.cursor and ~/.config/cursor",
}

// signInCLIInvocation matches a call of one of hiddenFromNextMinor's binaries,
// bare or by a path (~/.local/bin/gemini), where a command can start.
var signInCLIInvocation = regexp.MustCompile(`(?:^|[;&|({]\s*|\$\(\s*|\b(?:then|do|if|exec|timeout\s+\S+|!)\s+)(?:[^\s'"();&|]*/)?(gemini|cursor-agent)(?:\s|$|\))`)

// checkCredentialsNoneSignIns: under `credentials: none` a run command that
// calls gemini or cursor-agent runs signed in today, and signed out from the
// next minor release, which hides their sign-ins from that sandbox. The
// finding keeps its rule ID then, saying "hides" instead of "will hide", so a
// lint:allow for it keeps holding.
func (c *checker) checkCredentialsNoneSignIns(path, cmd, credentials string) {
	if !strings.EqualFold(strings.TrimSpace(credentials), "none") {
		return
	}
	var bins []string
	seen := map[string]bool{}
	for _, line := range strings.Split(joinContinuations(cmd), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, loc := range signInCLIInvocation.FindAllStringSubmatchIndex(line, -1) {
			bin := line[loc[2]:loc[3]]
			if seen[bin] || insideQuotes(line[:loc[2]]) {
				continue
			}
			seen[bin] = true
			bins = append(bins, bin)
		}
	}
	if len(bins) == 0 {
		return
	}
	sort.Strings(bins)
	dirs := make([]string, len(bins))
	for i, b := range bins {
		dirs[i] = hiddenFromNextMinor[b]
	}
	c.add(RuleCredentialsNoneSignIn, path,
		fmt.Sprintf("credentials: none will hide %s from the command in the next minor release, so %s will run signed out (the step prints a notice saying so each time it runs)",
			strings.Join(dirs, ", "), strings.Join(bins, ", ")),
		"run it with credentials: inherit (the CLI then uses its sign-in on the machine)")
}
