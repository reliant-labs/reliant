package obsout

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Table writes an aligned plain-text table: the human form of a list verb.
// Cells are single-line (newlines and tabs become spaces) and a cell wider than
// maxCell is cut with an ellipsis, so one long log body cannot wrap the table.
// maxCell <= 0 means no cut.
func Table(w io.Writer, header []string, rows [][]string, maxCell int) {
	clean := func(s string) string {
		s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s)
		if maxCell > 0 && utf8.RuneCountInString(s) > maxCell {
			r := []rune(s)
			s = string(r[:maxCell-1]) + "…"
		}
		return s
	}
	width := make([]int, len(header))
	grid := make([][]string, 0, len(rows)+1)
	grid = append(grid, header)
	for _, r := range rows {
		row := make([]string, len(header))
		for i := range header {
			if i < len(r) {
				row[i] = clean(r[i])
			}
		}
		grid = append(grid, row)
	}
	for _, r := range grid {
		for i, c := range r {
			if n := utf8.RuneCountInString(c); n > width[i] {
				width[i] = n
			}
		}
	}
	for _, r := range grid {
		var b strings.Builder
		for i, c := range r {
			if i == len(r)-1 {
				b.WriteString(c)
				break
			}
			b.WriteString(c)
			b.WriteString(strings.Repeat(" ", width[i]-utf8.RuneCountInString(c)+2))
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}
