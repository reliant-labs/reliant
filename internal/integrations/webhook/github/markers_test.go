// Copyright (c) 2025 Reliant Labs
package github

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sha40 = "0123456789abcdef0123456789abcdef01234567"

func marker(name string, kv ...string) map[string]any {
	attrs := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		attrs[kv[i]] = kv[i+1]
	}
	return map[string]any{"name": name, "attrs": attrs}
}

func TestMarkersGrammar(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []any
	}{
		{"none", "Looks good", nil},
		{"empty", "", nil},
		{"one", "LGTM\n\n<!-- review-passed sha=" + sha40 + " -->", []any{marker("review-passed", "sha", sha40)}},
		{"no attrs", "<!-- merge-regen -->", []any{marker("merge-regen")}},
		{"several attrs", "<!-- review-rereview sha=" + sha40 + " of=3003 -->", []any{marker("review-rereview", "sha", sha40, "of", "3003")}},
		{"in order", "<!-- b -->\ntext\n<!-- a x=1 --> <!-- c y=2 -->", []any{marker("b"), marker("a", "x", "1"), marker("c", "y", "2")}},
		{"no blanks", "<!--review-close sha=abc-->", []any{marker("review-close", "sha", "abc")}},
		{"name runs into the close", "<!-- review-passed-->", []any{marker("review-passed")}},
		{"tabs and extra blanks", "<!--\t a:b_c   k=v\t-->", []any{marker("a:b_c", "k", "v")}},
		{"mid-line", "verdict: <!-- review-passed sha=abc --> done", []any{marker("review-passed", "sha", "abc")}},
		{"crlf", "LGTM\r\n<!-- review-passed sha=abc -->\r\n", []any{marker("review-passed", "sha", "abc")}},
		{"value keeps dashes and =", "<!-- x v=a-b=c--->", []any{marker("x", "v", "a-b=c-")}},
		{"a stray dash before the close", "<!-- x v=a --->", nil},
		{"value is non-ASCII", "<!-- x v=héllo -->", []any{marker("x", "v", "héllo")}},

		{"free text", "<!-- this is a note for humans -->", nil},
		{"free text with a pair", "<!-- note: see sha=abc -->", nil},
		{"upper-case name", "<!-- Review-passed sha=abc -->", nil},
		{"name starts with a digit", "<!-- 1x -->", nil},
		{"upper-case key", "<!-- x SHA=abc -->", nil},
		{"quoted value", `<!-- x k="a b" -->`, nil},
		{"empty value", "<!-- x k= -->", nil},
		{"bare key", "<!-- x k -->", nil},
		{"repeated key", "<!-- x k=1 k=2 -->", nil},
		{"json payload", `<!-- review-blockers sha=abc ["a"] -->`, nil},
		{"unclosed", "<!-- review-passed sha=abc", nil},
		{"split across lines", "<!-- review-passed\nsha=abc -->", nil},
		{"empty comment", "<!---->", nil},
		{"control character in value", "<!-- x k=a\x01b -->", nil},
		{"value runs into >", "<!-- x k=abc> -->", nil},
		{"name too long", "<!-- a" + strings.Repeat("b", 64) + " -->", nil},
		{"name at its limit", "<!-- a" + strings.Repeat("b", 63) + " -->", []any{marker("a" + strings.Repeat("b", 63))}},
		{"key too long", "<!-- x k" + strings.Repeat("e", 32) + "=1 -->", nil},
		{"value too long", "<!-- x k=" + strings.Repeat("v", 129) + " -->", nil},
		{"value at its limit", "<!-- x k=" + strings.Repeat("v", 128) + " -->", []any{marker("x", "k", strings.Repeat("v", 128))}},
		{"marker over 256 bytes", "<!-- x a=" + strings.Repeat("v", 128) + " b=" + strings.Repeat("w", 128) + " -->", nil},
		{"a bad marker does not hide the next", "<!-- free text --> <!-- ok -->", []any{marker("ok")}},

		{"fenced", "```\n<!-- review-passed sha=abc -->\n```", nil},
		{"fenced with info and indent", "  ```markdown\n<!-- review-passed sha=abc -->\n  ```\n<!-- after -->", []any{marker("after")}},
		{"tilde fence", "~~~\n<!-- review-passed sha=abc -->\n~~~", nil},
		{"a shorter run does not close", "````\n```\n<!-- inside -->\n````\n<!-- outside -->", []any{marker("outside")}},
		{"other fence char does not close", "```\n~~~\n<!-- inside -->\n```\n<!-- outside -->", []any{marker("outside")}},
		{"unclosed fence hides the rest", "```\n<!-- inside -->", nil},
		{"inline code", "the review leaves `<!-- review-passed sha=abc -->` at its end", nil},
		{"double-backtick span", "``<!-- x -->`` <!-- y -->", []any{marker("y")}},
		{"after a closed span", "`code` <!-- y -->", []any{marker("y")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, markers(c.body))
		})
	}
}

func TestMarkersAreBounded(t *testing.T) {
	var b strings.Builder
	for i := range 20 {
		fmt.Fprintf(&b, "<!-- m n=%d -->\n", i)
	}
	got := markers(b.String())
	require.Len(t, got, maxMarkers)
	for i, m := range got {
		assert.Equal(t, marker("m", "n", fmt.Sprint(i)), m, "body order")
	}

	// Many on one line stop at the bound too.
	got = markers(strings.Repeat("<!-- m --> ", 40))
	assert.Len(t, got, maxMarkers)
}

// The body is untrusted: a megabyte built to make a scanner backtrack is read
// in one pass.
func TestMarkersScanIsLinear(t *testing.T) {
	const mb = 1 << 20
	inputs := map[string]string{
		"openers":        strings.Repeat("<!--", mb/4),
		"open names":     strings.Repeat("<!-- a ", mb/7),
		"open pairs":     strings.Repeat("<!-- a k=v ", mb/11),
		"long value":     "<!-- a k=" + strings.Repeat("v", mb),
		"dashes":         "<!-- a k=" + strings.Repeat("-", mb),
		"backtick runs":  strings.Repeat("`` ` <!-- ", mb/10),
		"fence lines":    strings.Repeat("```\n<!--\n", mb/9),
		"one long line":  strings.Repeat("<!-- free text that never closes ", mb/34),
		"blank-filled":   "<!--" + strings.Repeat(" ", mb),
		"marker storm":   strings.Repeat("<!-- m k=v -->", mb/14),
		"newline storm":  strings.Repeat("\n", mb),
		"unclosed fence": "```\n" + strings.Repeat("<!-- m -->\n", mb/11),
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			got := markers(in)
			assert.LessOrEqual(t, len(got), maxMarkers)
			// A linear scan of 1 MB takes milliseconds; a quadratic one,
			// minutes. The bound leaves room for a loaded machine.
			assert.Less(t, time.Since(start), 2*time.Second)
		})
	}
}

func reviewWithBody(t *testing.T, body string) []byte {
	t.Helper()
	var raw map[string]any
	require.NoError(t, json.Unmarshal(fixture(t, "pull_request_review.submitted"), &raw))
	raw["review"].(map[string]any)["body"] = body
	b, err := json.Marshal(raw)
	require.NoError(t, err)
	return b
}

// The reason the field exists: a long review keeps its verdict marker,
// although the body itself is cut at the text cap.
func TestReviewMarkersSurviveTheTextCap(t *testing.T) {
	body := strings.Repeat("A finding the author should read.\n", 40<<10/34) +
		"<!-- review-changes sha=" + sha40 + " -->\n"
	require.Greater(t, len(body), 40<<10)

	ev := one(t, parseBody(t, "pull_request_review", reviewWithBody(t, body)))
	review := ev.Data["review"].(map[string]any)
	capped := review["body"].(string)
	assert.True(t, strings.HasSuffix(capped, truncatedSuffix), "the body is capped")
	assert.NotContains(t, capped, "review-changes", "the marker is past the cap")
	assert.Equal(t, []any{marker("review-changes", "sha", sha40)}, review["markers"])
}

func TestMarkersFieldIsAbsentWithoutMarkers(t *testing.T) {
	ev := one(t, parseFixture(t, "pull_request_review", "pull_request_review.submitted"))
	_, ok := ev.Data["review"].(map[string]any)["markers"]
	assert.False(t, ok, "a body with no markers has no markers key")

	ev = one(t, parseFixture(t, "issue_comment", "issue_comment.created"))
	_, ok = ev.Data["comment"].(map[string]any)["markers"]
	assert.False(t, ok)

	ev = one(t, parseFixture(t, "pull_request_review_comment", "pull_request_review_comment.created"))
	_, ok = ev.Data["comment"].(map[string]any)["markers"]
	assert.False(t, ok)
}

func TestMarkersOfTheRecordedFixtures(t *testing.T) {
	ev := one(t, parseFixture(t, "pull_request_review", "pull_request_review.submitted.markers"))
	assert.Equal(t, []any{
		marker("review-round", "sha", strings.Repeat("a", 40), "kind", "code"),
		marker("review-passed", "sha", strings.Repeat("a", 40)),
	}, ev.Data["review"].(map[string]any)["markers"], "the fenced marker for another sha is not one")

	ev = one(t, parseFixture(t, "issue_comment", "issue_comment.created.markers"))
	assert.Equal(t, []any{marker("merge-regen", "of", strings.Repeat("a", 40))}, ev.Data["comment"].(map[string]any)["markers"])

	ev = one(t, parseFixture(t, "pull_request_review_comment", "pull_request_review_comment.created.markers"))
	assert.Equal(t, []any{marker("review-finding", "id", "f1", "severity", "blocker")}, ev.Data["comment"].(map[string]any)["markers"])
}
