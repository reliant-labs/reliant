package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMoneyFormattersSurviveTheRowsCurrency runs the SHIPPED format-utils.ts
// under node against the currency values a real row can hold.
//
// The generated list and detail pages pass each row's own `currency` column
// to formatMoneyCents. Intl.NumberFormat THROWS a RangeError for anything
// that is not a well-formed ISO 4217 code — and the dev seed fills a string
// column with `sample_currency_<n>`, a user can save an empty one, and a typo
// is one keystroke away. The throw happened inside a table cell, so the
// whole Orders page fell to its error boundary ("Something went wrong") on
// the first `forge env up` of the README's first sixty seconds.
//
// Executed rather than string-matched: the failure is a runtime exception,
// and the only proof it cannot happen is calling the function.
func TestMoneyFormattersSurviveTheRowsCurrency(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH — skipping the money-formatter execution check")
	}
	if testing.Short() {
		t.Skip("spawns node; runs in full mode")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "format-utils.ts"), []byte(formatUtilsScaffold(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Node strips TypeScript's erasable syntax natively (v22.18+/v23.6+), so
	// the shipped file runs as-is. Each line is "<case>\t<result>" or
	// "<case>\tTHROWS <error>".
	const harness = `import { formatMoneyCents, formatMoneyWhole, formatMoneyInterval } from "./format-utils.ts";
const cases: Array<[string, string | null | undefined]> = [
  ["seeded", "sample_currency_18"],
  ["empty", ""],
  ["typo", "US DOLLARS"],
  ["null", null],
  ["usd", "USD"],
];
for (const [name, currency] of cases) {
  for (const [fn, call] of [
    ["cents", () => formatMoneyCents(1899n, currency)],
    ["whole", () => formatMoneyWhole(1899n, currency)],
    ["interval", () => formatMoneyInterval(1899n, "month", currency)],
  ] as Array<[string, () => string]>) {
    try {
      console.log(name + "/" + fn + "\t" + call());
    } catch (e) {
      console.log(name + "/" + fn + "\tTHROWS " + (e as Error).name + ": " + (e as Error).message);
    }
  }
}
`
	if err := os.WriteFile(filepath.Join(dir, "harness.mts"), []byte(harness), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "harness.mts")
	cmd.Dir = dir
	// The expectations below are en-US digit grouping; pin the locale Intl
	// defaults to so a machine set to another one does not flip them.
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "Unknown file extension") || strings.Contains(string(out), "ERR_UNKNOWN_FILE_EXTENSION") {
			t.Skipf("this node cannot run TypeScript directly:\n%s", out)
		}
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}

	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, result, _ := strings.Cut(line, "\t")
		got[name] = result
		if strings.HasPrefix(result, "THROWS") {
			t.Errorf("%s: a row's currency made the formatter throw — one bad row blanks the whole page: %s", name, result)
		}
	}

	// What the unusable codes render as: the amount, with the raw value
	// beside it so the page still says what the row holds.
	for name, want := range map[string]string{
		"seeded/cents":    "18.99 sample_currency_18",
		"empty/cents":     "18.99",
		"null/cents":      "18.99",
		"seeded/whole":    "19 sample_currency_18",
		"seeded/interval": "18.99 sample_currency_18/mo",
	} {
		if got[name] != want {
			t.Errorf("%s = %q, want %q", name, got[name], want)
		}
	}
	// A real code still formats as currency.
	if !strings.Contains(got["usd/cents"], "$") || !strings.Contains(got["usd/cents"], "18.99") {
		t.Errorf("usd/cents = %q, want a $ amount", got["usd/cents"])
	}
}
