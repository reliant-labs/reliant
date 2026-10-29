package tmpl

import (
	"math"
	"strings"
	"unicode/utf8"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// lineWindowFn is `<string>.lineWindow(start_line, start_column, line_count,
// budget)`: the text from line start_line, byte start_column (1-based) of that
// line, through line start_line+line_count-1, as a map
//
//	{content, start_line, start_column, end_line, total_lines, truncated,
//	 next_line, next_column}
//
// so a manifest can serve a large file one window at a time. line_count <= 0
// means "to the end". budget bounds the window's size as it will be delivered
// — JSON-encoded, the way an action result reaches an agent — so a window
// never runs into the action layer's character cut and arrives with its
// middle missing: the window stops at the last whole line that fits and
// truncated says the caller got less than it asked for.
//
// A line that alone exceeds the budget (a minified file) is delivered in
// pieces: the window holds as much of it as fits, end_line is that line, and
// next_column is the byte of it to continue from. next_line/next_column name
// exactly where the following window starts (null when nothing follows), so
// calling again from there, repeatedly, reconstructs the file byte for byte.
//
// Lines are counted the way an editor numbers them: "a\nb\n" has two. A
// start_line past the end answers an empty window with end_line =
// start_line - 1.
var lineWindowFn = cel.Function("lineWindow",
	cel.MemberOverload("string_line_window_dyn_dyn_dyn_dyn",
		[]*cel.Type{cel.StringType, cel.DynType, cel.DynType, cel.DynType, cel.DynType}, cel.DynType,
		cel.FunctionBinding(func(args ...ref.Val) ref.Val {
			text, ok := args[0].(types.String)
			if !ok {
				return types.MaybeNoSuchOverloadErr(args[0])
			}
			var n [4]int64
			for i := range n {
				v, ok := celInt(args[i+1])
				if !ok {
					return types.MaybeNoSuchOverloadErr(args[i+1])
				}
				n[i] = v
			}
			return types.DefaultTypeAdapter.NativeToValue(LineWindow(string(text), n[0], n[1], n[2], n[3]))
		})))

// celInt reads a whole number from a CEL value: JSON params arrive as
// doubles, literals as ints.
func celInt(v ref.Val) (int64, bool) {
	switch n := v.(type) {
	case types.Int:
		return int64(n), true
	case types.Uint:
		if uint64(n) > math.MaxInt64 {
			return math.MaxInt64, true
		}
		return int64(n), true
	case types.Double:
		f := float64(n)
		if f != math.Trunc(f) || math.IsNaN(f) {
			return 0, false
		}
		if f > math.MaxInt64 {
			return math.MaxInt64, true
		}
		if f < math.MinInt64 {
			return math.MinInt64, true
		}
		return int64(f), true
	}
	return 0, false
}

// LineWindow is lineWindow's implementation; see lineWindowFn.
func LineWindow(text string, start, col, count, budget int64) map[string]any {
	if start < 1 {
		start = 1
	}
	if col < 1 {
		col = 1
	}
	// lineStarts[i] is the byte offset of line i+1.
	lineStarts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' && i+1 < len(text) {
			lineStarts = append(lineStarts, i+1)
		}
	}
	total := int64(len(lineStarts))
	if text == "" {
		total = 0
	}
	lineEnd := func(n int64) int { // byte offset just past line n
		if n >= total {
			return len(text)
		}
		return lineStarts[n]
	}
	window := func(content string, end int64, truncated bool, nextLine, nextCol any) map[string]any {
		return map[string]any{
			"content":      content,
			"start_line":   start,
			"start_column": col,
			"end_line":     end,
			"total_lines":  total,
			"truncated":    truncated,
			"next_line":    nextLine,
			"next_column":  nextCol,
		}
	}
	if start > total {
		return window("", start-1, false, nil, nil)
	}
	last := total
	if count > 0 && start-1+count < total {
		last = start - 1 + count
	}
	// following is where the window after one ending just past line end starts.
	following := func(end int64) (any, any) {
		if end >= total {
			return nil, nil
		}
		return end + 1, int64(1)
	}

	from := lineStarts[start-1]
	if off := int64(lineEnd(start) - from); col-1 > off {
		col = off + 1
	}
	from += int(col - 1)
	for from < lineEnd(start) && !utf8.RuneStart(text[from]) {
		from++ // never start inside a rune
		col++
	}
	used := int64(0)
	end := start - 1
	for n := start; n <= last; n++ {
		segStart := lineStarts[n-1]
		if n == start {
			segStart = from
		}
		cost := jsonCost(text[segStart:lineEnd(n)])
		if budget > 0 && used+cost > budget {
			break
		}
		used += cost
		end = n
	}
	if end < start {
		// The first line alone exceeds the budget: deliver as much of it as
		// fits, and say which byte of it the next window starts at.
		piece := cutToCost(text[from:lineEnd(start)], budget)
		if piece == "" {
			_, size := utf8.DecodeRuneInString(text[from:])
			piece = text[from : from+size] // always advance
		}
		return window(piece, start, true, start, col+int64(len(piece)))
	}
	nl, nc := following(end)
	return window(text[from:lineEnd(end)], end, end < last, nl, nc)
}

// jsonCost is how many bytes s occupies inside a JSON string as
// encoding/json writes it (HTML-safe escaping included).
func jsonCost(s string) int64 {
	var n int64
	for _, r := range s {
		n += runeJSONCost(r)
	}
	return n
}

func runeJSONCost(r rune) int64 {
	switch {
	case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t':
		return 2
	case r < 0x20 || r == '<' || r == '>' || r == '&' || r == '\u2028' || r == '\u2029' || r == utf8.RuneError:
		return 6
	default:
		return int64(utf8.RuneLen(r))
	}
}

// cutToCost is the longest prefix of s, on a rune boundary, whose JSON cost
// fits budget.
func cutToCost(s string, budget int64) string {
	var b strings.Builder
	var used int64
	for _, r := range s {
		c := runeJSONCost(r)
		if used+c > budget {
			break
		}
		used += c
		b.WriteRune(r)
	}
	return b.String()
}
