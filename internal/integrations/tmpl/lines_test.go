package tmpl

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLineWindow(t *testing.T) {
	type want struct {
		content         string
		start, end, tot int64
		truncated       bool
	}
	for name, tc := range map[string]struct {
		text                 string
		start, count, budget int64
		want                 want
	}{
		"whole file":                {"a\nb\nc\n", 1, 0, 1000, want{"a\nb\nc\n", 1, 3, 3, false}},
		"no trailing newline":       {"a\nb", 1, 0, 1000, want{"a\nb", 1, 2, 2, false}},
		"middle window":             {"a\nb\nc\nd\n", 2, 2, 1000, want{"b\nc\n", 2, 3, 4, false}},
		"count past the end":        {"a\nb\n", 2, 50, 1000, want{"b\n", 2, 2, 2, false}},
		"start past the end":        {"a\nb\n", 5, 0, 1000, want{"", 5, 4, 2, false}},
		"start below one":           {"a\n", 0, 0, 1000, want{"a\n", 1, 1, 1, false}},
		"empty file":                {"", 1, 0, 1000, want{"", 1, 0, 0, false}},
		"budget stops on a line":    {"aaaa\nbbbb\ncccc\n", 1, 0, 12, want{"aaaa\nbbbb\n", 1, 2, 3, true}},
		"escapes count toward cost": {"<<\n>>\n", 1, 0, 14, want{"<<\n", 1, 1, 2, true}},
		"oversized first line":      {"abcdefghij\nk\n", 1, 0, 4, want{"abcd", 1, 1, 2, true}},
	} {
		t.Run(name, func(t *testing.T) {
			got := LineWindow(tc.text, tc.start, 1, tc.count, tc.budget)
			g := want{got["content"].(string), got["start_line"].(int64), got["end_line"].(int64), got["total_lines"].(int64), got["truncated"].(bool)}
			if g != tc.want {
				t.Fatalf("got %+v want %+v", g, tc.want)
			}
		})
	}
}

// The budget is what the window costs once JSON-encoded, which is how an
// action result reaches an agent: a window never outgrows it.
func TestLineWindowBudgetIsTheEncodedSize(t *testing.T) {
	text := strings.Repeat("if a < b && \"q\" != `\\` {\t}\n", 5000)
	got := LineWindow(text, 1, 1, 0, 24000)
	enc, err := json.Marshal(got["content"])
	if err != nil {
		t.Fatal(err)
	}
	if n := len(enc) - 2; n > 24000 || n < 23000 {
		t.Fatalf("encoded window is %d bytes; want it filled to just under 24000", n)
	}
}

func TestLineWindowFromCEL(t *testing.T) {
	got, err := EvalExpr(`cel.bind(w, params.text.lineWindow(params.start, 1, 2.0, 1000), w.content + string(w.total_lines))`,
		map[string]any{"params": map[string]any{"text": "a\nb\nc\n", "start": 2.0}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "b\nc\n3" {
		t.Fatalf("got %q", got)
	}
}

// Following next_line/next_column from the first window reconstructs the
// file byte for byte, including lines longer than one window (a minified
// file, a long line between ordinary ones) and multi-byte runes at a cut.
func TestLineWindowPagingReconstructsTheFile(t *testing.T) {
	for name, text := range map[string]string{
		"one oversized line":             strings.Repeat("x", 25000),
		"oversized line between others":  "a\n" + strings.Repeat("é<", 9000) + "\nb\nc",
		"ordinary lines":                 strings.Repeat("line of text\n", 4000),
		"oversized last line no newline": "head\n" + strings.Repeat("y", 50001),
	} {
		t.Run(name, func(t *testing.T) {
			var got strings.Builder
			line, col := int64(1), int64(1)
			for calls := 0; ; calls++ {
				if calls > 1000 {
					t.Fatal("paging did not terminate")
				}
				w := LineWindow(text, line, col, 0, 24000)
				got.WriteString(w["content"].(string))
				if w["next_line"] == nil {
					break
				}
				nl, nc := w["next_line"].(int64), w["next_column"].(int64)
				if nl < line || (nl == line && nc <= col) {
					t.Fatalf("window at %d:%d did not advance (next %d:%d)", line, col, nl, nc)
				}
				line, col = nl, nc
			}
			if got.String() != text {
				t.Fatalf("reconstructed %d bytes, want %d", got.Len(), len(text))
			}
		})
	}
}
