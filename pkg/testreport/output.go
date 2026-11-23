package testreport

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/redact"
)

// truncationMarker joins the kept head and tail of cut output.
const truncationMarker = "\n… [output truncated] …\n"

// boundedText keeps the first and last halves of what is written to it, up
// to max bytes in all, so a test that prints a megabyte keeps both its
// assertion (near the start) and its panic (at the end).
type boundedText struct {
	max   int
	head  []byte
	tail  []byte // ring buffer once full
	start int    // ring start
	full  bool
	total int64
}

func newBoundedText(max int) *boundedText { return &boundedText{max: max} }

func (b *boundedText) write(s string) {
	b.total += int64(len(s))
	half := b.max / 2
	if n := half - len(b.head); n > 0 {
		take := min(n, len(s))
		b.head = append(b.head, s[:take]...)
		s = s[take:]
	}
	tailCap := b.max - half
	for len(s) > 0 && tailCap > 0 {
		if !b.full {
			take := min(tailCap-len(b.tail), len(s))
			b.tail = append(b.tail, s[:take]...)
			s = s[take:]
			if len(b.tail) == tailCap {
				b.full = true
			}
			continue
		}
		// Overwrite the oldest bytes. Only the last tailCap bytes of s
		// can survive, so skip the rest.
		if len(s) > tailCap {
			s = s[len(s)-tailCap:]
		}
		for i := 0; i < len(s); i++ {
			b.tail[b.start] = s[i]
			b.start = (b.start + 1) % tailCap
		}
		s = ""
	}
}

func (b *boundedText) truncated() bool { return b.total > int64(len(b.head)+len(b.tail)) }

func (b *boundedText) size() int { return len(b.head) + len(b.tail) }

func (b *boundedText) String() string {
	tail := b.tail
	if b.full && b.start > 0 {
		tail = append(append([]byte(nil), b.tail[b.start:]...), b.tail[:b.start]...)
	}
	if b.truncated() {
		return strings.ToValidUTF8(string(b.head), "") + truncationMarker + strings.ToValidUTF8(string(tail), "")
	}
	return string(b.head) + string(tail)
}

// scrub removes credential shapes from output that will be stored or sent.
func scrub(s string) string {
	return redact.Secrets(strings.ToValidUTF8(s, "\uFFFD"))
}

// failureLoc finds the first `name_test.go:88:` style location in output.
var failureLoc = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9_.\-]+\.(?:go|ts|tsx|js|jsx)):(\d+):`)

func locate(output string) (string, int) {
	m := failureLoc.FindStringSubmatch(output)
	if m == nil {
		return "", 0
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0
	}
	return m[1], n
}

// outputBudget is the run-wide bound on kept failure output.
type outputBudget struct{ left int64 }

// keep finalises one failure's output against the budget: scrubbed, and
// dropped (truncated) once the budget is spent.
func (o *outputBudget) keep(raw string, wasTruncated bool) (string, bool) {
	if raw == "" {
		return "", wasTruncated
	}
	if int64(len(raw)) > o.left {
		return "", true
	}
	o.left -= int64(len(raw))
	return scrub(raw), wasTruncated
}
