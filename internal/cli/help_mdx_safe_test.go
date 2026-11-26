// File: internal/cli/help_mdx_safe_test.go
//
// Every command's help must survive being rendered into an MDX page. Hosts
// that embed forge's tree publish it as a CLI reference: reliant's
// tools/docgen/cli walks `reliant forge …` (visible commands and flags only)
// and writes Short, Long and each flag's Usage into markdown as prose, the
// usage line in a fenced block, and Example not at all. Its Mintlify step
// (scripts/generate-mintlify-reference.py) then escapes prose for MDX line by
// line: every `<` outside a backtick span becomes `\<`, every brace `\{`.
//
// So the `<env>` placeholders all over forge's help are fine there, and this
// test does not flag them (there are hundreds, and every MDX consumer of
// plain-text help has to escape prose anyway). It flags the two shapes no
// escaping repairs, each of which made a whole page refuse to build:
//
//   - A bare URL that is not a real URL. GFM turns `http://…` in prose into a
//     link, and Mintlify refuses a page whose link does not parse. A template
//     URL is worse: `http://127.0.0.1:<pprof port>/…` (forge bug 70b9e58e,
//     `forge test collect`) escapes to `http://127.0.0.1:\<pprof…`, an autolink
//     ending in a backslash, and reliant's `task generate:docs` failed on it.
//     A URL with a placeholder belongs in backticks: then it is code, not a link.
//   - A backtick span that opens on one line and closes on the next. Markdown
//     allows it, but a line-by-line escaper sees the second line's code as
//     prose and its prose as code, so a `<env>` after the close is left raw
//     and parses as an unclosed JSX tag.

package cli

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// bareURLRe finds a URL in prose. It runs to the next whitespace, which is
// where a GFM autolink ends too (after trailing punctuation is dropped).
var bareURLRe = regexp.MustCompile(`https?://\S+`)

// mdxHelpProblems returns why text would break an MDX page, one entry per
// problem, or nil.
func mdxHelpProblems(text string) []string {
	var problems []string
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.Count(line, "`")%2 == 1 {
			problems = append(problems, "a backtick span does not close on its own line (keep each `…` on one line): "+strings.TrimSpace(line))
			continue
		}
		parts := strings.Split(line, "`")
		for i := 0; i < len(parts); i += 2 { // even parts are outside code
			for _, raw := range bareURLRe.FindAllString(parts[i], -1) {
				u := strings.TrimRight(raw, `.,;:!?)"'`)
				parsed, err := url.Parse(u)
				if err != nil || parsed.Host == "" || strings.ContainsAny(u, "<>{}") {
					problems = append(problems, "bare URL "+u+" is not a real URL (wrap a URL template in backticks): "+strings.TrimSpace(line))
				}
			}
		}
	}
	return problems
}

func TestMDXHelpProblems(t *testing.T) {
	bad := map[string]string{
		"placeholder in URL (70b9e58e)": "  host     GET http://127.0.0.1:<pprof port>/debug/coverage, plus",
		"placeholder port":              "an https://host[:port] override",
		"brace template":                "dial http://localhost:{port}/x",
		"span across lines":             "what `forge test\ncoverage --json` prints",
	}
	for name, text := range bad {
		if len(mdxHelpProblems(text)) == 0 {
			t.Errorf("%s: %q passed, want a problem", name, text)
		}
	}
	good := map[string]string{
		"scheme alone":          "an https:// address (GFM links only a URL with a host)",
		"real URL":              "(https://admin.reliantapi.com)",
		"URL with port":         "served at http://localhost:8080/debug.",
		"URL template in code":  "GET `http://127.0.0.1:<pprof port>/debug/coverage`, plus",
		"placeholders in prose": "forge env deploy <env> [--release <v>] {rule: off}",
		"fenced block":          "```\nhttp://127.0.0.1:<port>/x `open\n```",
		"span on one line":      "what `forge test coverage --json` prints",
	}
	for name, text := range good {
		if p := mdxHelpProblems(text); len(p) != 0 {
			t.Errorf("%s: %q flagged: %v", name, text, p)
		}
	}
}

// TestEveryCommandHelpIsMDXSafe walks the assembled tree as a CLI reference
// renders it: visible commands, their visible flags, and the text rendered as
// prose. Use is excluded (rendered in a fenced block, so `<name>` is safe
// there), and so is Example (fenced or not rendered at all).
func TestEveryCommandHelpIsMDXSafe(t *testing.T) {
	root := newTestRootCmd(t)
	var walk func(c *cobra.Command, path string)
	report := func(where, text string) {
		for _, p := range mdxHelpProblems(text) {
			t.Errorf("%s: %s", where, p)
		}
	}
	walk = func(c *cobra.Command, path string) {
		report(path+" Short", c.Short)
		report(path+" Long", c.Long)
		c.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
			if !f.Hidden {
				report(path+" --"+f.Name, f.Usage)
			}
		})
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			walk(sub, path+" "+sub.Name())
		}
	}
	walk(root, root.Name())
}
