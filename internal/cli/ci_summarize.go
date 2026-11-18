package cli

// `forge ci summarize <doc.json>...`: render forge --json documents as Markdown
// for a CI job summary ($GITHUB_STEP_SUMMARY), so a scaffolded workflow carries
// no bespoke jq (ADR docs/adr/env-verbs.md, task V6).
//
// NO LOGIC, BY DESIGN. It never decides anything and never fails a step over a
// document's CONTENT — the step that produced the document already exited with
// the verdict, and that exit code is the control flow (§3.6: "exit codes are the
// control flow; no jq parses free text"). A summary that exited non-zero on a
// failed rollout would fail the job twice, the second time in a step whose name
// says nothing about why. It exits 1 only when it cannot READ an input: a
// missing file or one that is not a JSON object.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func newCISummarizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "summarize <doc.json>...",
		Short: "Render forge --json documents as a Markdown job summary (for $GITHUB_STEP_SUMMARY)",
		Long: `Render one or more forge --json documents as Markdown, for a CI job summary.

Every hosted deploy verb's --json carries the same envelope (ok, exit_code,
error); this prints that as a one-line verdict, the document's scalar fields as
a table, and its row lists (images, workloads, stages, promotions, gates) as
tables. It reads documents; it decides nothing.

It exits 0 whatever the documents say — the step that wrote each document
already exited with its verdict. It exits 1 only when an input cannot be read.

Examples:
  forge env deploy prod v1.4.0 --json > deploy.json
  forge env status prod --json > status.json
  forge ci summarize deploy.json status.json >> "$GITHUB_STEP_SUMMARY"`,
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCISummarize(args, cmd.OutOrStdout())
		},
	}
}

func runCISummarize(paths []string, out io.Writer) error {
	var unreadable []string
	for i, p := range paths {
		if i > 0 {
			fmt.Fprintln(out)
		}
		raw, err := os.ReadFile(p) //nolint:gosec // the caller names the documents to summarize
		if err != nil {
			unreadable = append(unreadable, fmt.Sprintf("%s: %v", p, err))
			fmt.Fprintf(out, "### %s\n\n⚠️ could not read: %v\n", filepath.Base(p), err)
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			unreadable = append(unreadable, fmt.Sprintf("%s: not a JSON object: %v", p, err))
			fmt.Fprintf(out, "### %s\n\n⚠️ not a forge JSON document: %v\n", filepath.Base(p), err)
			continue
		}
		renderSummary(out, filepath.Base(p), doc)
	}
	if len(unreadable) > 0 {
		return errors.New("could not summarize: " + strings.Join(unreadable, "; "))
	}
	return nil
}

// summaryRowKeys are the row lists the hosted verbs emit, rendered as tables
// in this order when present.
var summaryRowKeys = []string{"stages", "workloads", "images", "promotions", "environments", "gates", "unpinned"}

func renderSummary(out io.Writer, name string, doc map[string]any) {
	fmt.Fprintf(out, "### %s\n\n", name)
	fmt.Fprintln(out, summaryVerdict(doc))
	fmt.Fprintln(out)

	// Scalars: everything that is not the envelope and not a row list.
	skip := map[string]bool{"ok": true, "exit_code": true, "error": true}
	for _, k := range summaryRowKeys {
		skip[k] = true
	}
	var keys []string
	for k, v := range doc {
		if skip[k] {
			continue
		}
		if _, isScalar := summaryScalar(v); isScalar {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		fmt.Fprintln(out, "| field | value |")
		fmt.Fprintln(out, "|---|---|")
		for _, k := range keys {
			s, _ := summaryScalar(doc[k])
			fmt.Fprintf(out, "| %s | %s |\n", k, mdCell(s))
		}
		fmt.Fprintln(out)
	}

	for _, k := range summaryRowKeys {
		rows, ok := doc[k].([]any)
		if !ok || len(rows) == 0 {
			continue
		}
		renderSummaryRows(out, k, rows)
	}
}

// summaryVerdict renders the F0 envelope as one line. A document carrying no
// envelope says so rather than implying a pass.
func summaryVerdict(doc map[string]any) string {
	okVal, hasOK := doc["ok"].(bool)
	code, hasCode := doc["exit_code"].(float64)
	msg, _ := doc["error"].(string)
	switch {
	case !hasOK && !hasCode:
		return "**verdict:** _(no ok/exit_code in this document)_"
	case hasOK && okVal:
		return "**verdict:** ✅ ok (exit 0)"
	default:
		line := fmt.Sprintf("**verdict:** ❌ exit %d", int(code))
		if msg != "" {
			line += " — " + mdCell(firstLine(msg))
		}
		return line
	}
}

// renderSummaryRows prints a list of objects as a table whose columns are the
// union of their scalar fields, in first-seen order.
func renderSummaryRows(out io.Writer, title string, rows []any) {
	var cols []string
	seen := map[string]bool{}
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, isScalar := summaryScalar(m[k]); isScalar && !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
		}
	}
	if len(cols) == 0 {
		return
	}
	fmt.Fprintf(out, "**%s**\n\n| %s |\n|%s\n", title, strings.Join(cols, " | "), strings.Repeat("---|", len(cols)))
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		cells := make([]string, len(cols))
		for i, c := range cols {
			s, _ := summaryScalar(m[c])
			cells[i] = mdCell(s)
		}
		fmt.Fprintf(out, "| %s |\n", strings.Join(cells, " | "))
	}
	fmt.Fprintln(out)
}

// summaryScalar renders a JSON scalar; objects and arrays are not scalars.
func summaryScalar(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", true
	case string:
		return x, true
	case bool:
		return fmt.Sprint(x), true
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprint(int64(x)), true
		}
		return fmt.Sprint(x), true
	default:
		return "", false
	}
}

// mdCell keeps a value inside one table cell.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", " ")
}
