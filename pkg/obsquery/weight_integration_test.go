//go:build integration

package obsquery_test

// The sampling threshold's string-function fast path against its regex
// definition, in a real ClickHouse: the same value for every tracestate,
// the shapes the sampler writes, the malformed ones near them, and a few
// thousand random strings over the characters the grammar turns on.
//
//	go test -tags integration -run SpanThreshold ./pkg/obsquery/

import (
	"context"
	"math/rand"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestSpanThresholdFastPathMatchesTheRegex(t *testing.T) {
	c := startClickHouse(t)
	states := []string{
		"", "ot=th:0", "ot=th:e6666666666666", "ot=th:", "ot=th:E6", "ot=th:e6z", "ot=th:-1",
		"ot=rv:abc;th:8", "foo=bar,ot=th:8", "ot=th:8,foo=1", "ot=th:8;rv:1", "xot=th:8", " ot=th:8",
		"ot=th:8 ", "ot=th:th:8", "ot=th:0000000000000000000000", "ot=th:8,ot=th:9", "ot=rv:1;th:2;th:3",
		// Quotes and backslashes: the literal escaping below must carry
		// them through unchanged.
		"ot=th:8'", `ot=th:\8`, `ot=th:8\`, `foo='\',ot=th:8`,
	}
	r := rand.New(rand.NewSource(1))
	const alphabet = "ot=h:0e6fzE,; "
	for range 5000 {
		var b strings.Builder
		if r.Intn(2) == 0 {
			b.WriteString("ot=th:")
		}
		for n := r.Intn(12); n > 0; n-- {
			b.WriteByte(alphabet[r.Intn(len(alphabet))])
		}
		states = append(states, b.String())
	}
	quoted := make([]string, len(states))
	for i, s := range states {
		quoted[i] = clickHouseString(s)
	}
	q := "SELECT TraceState, " + obsquery.SpanThresholdSQL + " AS fast, " + obsquery.SpanThresholdRegexSQL + " AS regex " +
		"FROM (SELECT arrayJoin([" + strings.Join(quoted, ", ") + "]) AS TraceState) WHERE fast != regex"
	rows, err := c.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s, fast, regex string
		if err := rows.Scan(&s, &fast, &regex); err != nil {
			t.Fatal(err)
		}
		t.Errorf("tracestate %q: fast path %q, regex %q", s, fast, regex)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

// clickHouseString is s as a ClickHouse string literal: a backslash escapes
// in one, so it is doubled before each quote is escaped.
func clickHouseString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(s) + "'"
}
