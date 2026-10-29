package tmpl

import (
	"math"
	"strings"
	"unicode/utf8"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// lineWindowFn is `<string>.lineWindow(start_line, line_count, budget)`: the
// lines [start_line, start_line+line_count) of a text, as a map
//
//	{content, start_line, end_line, total_lines, truncated}
//
// so a manifest can serve a large file one window at a time. line_count <= 0
// means "to the end". budget bounds the window's size as it will be delivered
// — JSON-encoded, the way an action result reaches an agent — so a window
// never runs into the action layer's character cut and arrives with its
// middle missing: the window stops at the last whole line that fits and
// truncated says the caller got fewer lines than it asked for. The first line
// is always delivered, cut to the budget when it alone is larger (a minified
// file), so every call makes progress. end_line < total_lines means more
// follows: the next window starts at end_line + 1.
//
// Lines are counted the way an editor numbers them: "a\nb\n" has two. A
// start_line past the end answers an empty window with end_line =
// start_line - 1.
var lineWindowFn = cel.Function("lineWindow",
	cel.MemberOverload("string_line_window_dyn_dyn_dyn",
		[]*cel.Type{cel.StringType, cel.DynType, cel.DynType, cel.DynType}, cel.DynType,
		cel.FunctionBinding(func(args ...ref.Val) ref.Val {
			text, ok := args[0].(types.String)
			if !ok {
				return types.MaybeNoSuchOverloadErr(args[0])
			}
			start, ok := celInt(args[1])
			if !ok {
				return types.MaybeNoSuchOverloadErr(args[1])
			}
			count, ok := celInt(args[2])
			if !ok {
				return types.MaybeNoSuchOverloadErr(args[2])
			}
			budget, ok := celInt(args[3])
			if !ok {
				return types.MaybeNoSuchOverloadErr(args[3])
			}
			return types.DefaultTypeAdapter.NativeToValue(LineWindow(string(text), start, count, budget))
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
func LineWindow(text string, start, count, budget int64) map[string]any {
	if start < 1 {
		start = 1
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
	window := func(content string, end int64, truncated bool) map[string]any {
		return map[string]any{
			"content":     content,
			"start_line":  start,
			"end_line":    end,
			"total_lines": total,
			"truncated":   truncated,
		}
	}
	if start > total {
		return window("", start-1, false)
	}
	last := total
	if count > 0 && start-1+count < total {
		last = start - 1 + count
	}
	lineEnd := func(n int64) int { // byte offset just past line n
		if n >= total {
			return len(text)
		}
		return lineStarts[n]
	}

	from := lineStarts[start-1]
	used := int64(0)
	end := start - 1
	for n := start; n <= last; n++ {
		cost := jsonCost(text[lineStarts[n-1]:lineEnd(n)])
		if budget > 0 && used+cost > budget {
			break
		}
		used += cost
		end = n
	}
	if end < start {
		// The first line alone exceeds the budget: deliver as much of it as
		// fits, so the caller still advances.
		line := text[from:lineEnd(start)]
		return window(cutToCost(line, budget), start, true)
	}
	return window(text[from:lineEnd(end)], end, end < last)
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
