package benefits

import (
	"fmt"
	"strings"
	"testing"
)

// reasonBase has a shipped entry in each of two categories, a hosted one, an
// in-progress one and a headline, so each rule below fails alone.
const reasonBase = validCategories + `benefits:
  - {id: gen, category: build, title: G, claim: Generates., availability: forge, headline: true, evidence: [{path: go.mod}]}
  - {id: schema, category: data, title: S, claim: Schemas., availability: forge, evidence: [{path: go.mod}]}
  - {id: gen-too, category: build, title: G2, claim: Generates more., availability: forge, evidence: [{path: go.mod}]}
  - {id: hosted-db, category: data, title: H, claim: Hosts., availability: hosted, evidence: [{path: go.mod}]}
  - {id: later, category: data, title: L, claim: Not yet., status: in-progress, availability: forge, evidence: [{path: go.mod}]}
`

// reason renders one reasons[] entry with the given availability and
// benefit ids, and otherwise valid fields.
func reason(id, availability string, ids ...string) string {
	return fmt.Sprintf("  - {id: %s, title: A searchable outcome, thesis: A short paragraph., mechanism: Because forge knows X it can Y., availability: %s, benefits: [%s]}\n",
		id, availability, strings.Join(ids, ", "))
}

func TestParseRejectsBadReasons(t *testing.T) {
	good := reasonBase + "reasons:\n" + reason("one", "forge", "gen", "schema")
	if _, err := Parse([]byte(good)); err != nil {
		t.Fatalf("the baseline registry must parse: %v", err)
	}
	// A hosted reason may stand on hosted entries; a forge one may not.
	if _, err := Parse([]byte(reasonBase + "reasons:\n" + reason("one", "hosted", "gen", "hosted-db"))); err != nil {
		t.Fatalf("a hosted reason backed at the hosted tier must parse: %v", err)
	}
	// With no reasons at all, the headline rule does not apply.
	if _, err := Parse([]byte(reasonBase)); err != nil {
		t.Fatalf("a registry with no reasons must parse: %v", err)
	}

	cases := []struct{ name, yaml, wantErr string }{
		{"unknown benefit", reasonBase + "reasons:\n" + reason("one", "forge", "gen", "schema", "nope"), `reasons[0] "one": benefit "nope" is not a benefits[].id`},
		{"in-progress benefit", reasonBase + "reasons:\n" + reason("one", "forge", "gen", "schema", "later"), `benefit "later" is in-progress`},
		{"one category", reasonBase + "reasons:\n" + reason("one", "forge", "gen", "gen-too"), "span 1 categories; a reason spans at least 2"},
		{"forge reason backed only by hosted", reasonBase + "reasons:\n" + reason("one", "forge", "gen", "hosted-db"), `at availability "forge" or below span 1 categories`},
		{"one benefit", reasonBase + "reasons:\n" + reason("one", "forge", "gen"), "1 benefits; a reason cites at least 2"},
		{"cited twice", reasonBase + "reasons:\n" + reason("one", "forge", "gen", "schema", "gen"), `benefit "gen" is cited twice`},
		{"headline no reason cites", reasonBase + "reasons:\n" + reason("one", "forge", "gen-too", "schema"), `benefits "gen": a headline no reason cites`},
		{"duplicate id", reasonBase + "reasons:\n" + reason("one", "forge", "gen", "schema") + reason("one", "forge", "gen", "schema"), `reasons[1] "one": duplicate reason id`},
		{"reserved anchor", reasonBase + "reasons:\n" + reason("limits", "forge", "gen", "schema"), "reserved for a docs/why-forge.md heading"},
		{"bad availability", reasonBase + "reasons:\n" + reason("one", "everywhere", "gen", "schema"), `availability "everywhere" is not one of`},
		{"mechanism shape", strings.Replace(reasonBase+"reasons:\n"+reason("one", "forge", "gen", "schema"), "Because forge", "Forge", 1), `mechanism must start with "Because "`},
		{"empty thesis", strings.Replace(reasonBase+"reasons:\n"+reason("one", "forge", "gen", "schema"), "A short paragraph.", "' '", 1), "thesis is empty"},
		{"long title", strings.Replace(reasonBase+"reasons:\n"+reason("one", "forge", "gen", "schema"), "A searchable outcome", strings.Repeat("x", 101), 1), "title is 101 bytes; at most 100"},
		{"show spells forge", strings.Replace(reasonBase+"reasons:\n"+reason("one", "forge", "gen", "schema"), "availability: forge, benefits", "availability: forge, show: forge lint, benefits", 1), "without the leading `forge`"},
		{"too many", reasonBase + "reasons:\n" + manyReasons(13), "13 reasons; at most 12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("Parse accepted it; want an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v\nwant it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func manyReasons(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(reason(fmt.Sprintf("r%d", i), "forge", "gen", "schema"))
	}
	return b.String()
}

// The embedded registry makes the argument: a full set of reasons, each
// rendered on the page and in the README block with its anchor.
func TestEmbeddedRegistryHasReasons(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r.Reasons); n < 8 || n > maxReasons {
		t.Fatalf("benefits.yaml has %d reasons; want 8 to %d", n, maxReasons)
	}
	page := string(RenderWhy(r))
	readme := string(RenderReadmeReasons(r))
	for _, rs := range r.Reasons {
		if !strings.Contains(page, `<a id="`+rs.ID+`"></a>`+"\n\n## "+rs.Title+"\n") {
			t.Errorf("reason %q has no anchor directly above its heading", rs.ID)
		}
		if !strings.Contains(readme, "]("+WhyPath+"#"+rs.ID+")") {
			t.Errorf("reason %q is not linked from the README block", rs.ID)
		}
		for _, id := range rs.Benefits {
			if !strings.Contains(page, "(benefits.md#"+id+")") {
				t.Errorf("reason %q: benefit %q is not linked on the page", rs.ID, id)
			}
		}
	}
	if !strings.Contains(page, `<a id="limits"></a>`) {
		t.Error("the page does not end with the limits")
	}
}

func TestWrapText(t *testing.T) {
	got := WrapText("one two three four", 11, "  ")
	if want := "  one two\n  three\n  four\n"; got != want {
		t.Errorf("WrapText = %q, want %q", got, want)
	}
	if got := WrapText("  ", 10, "  "); got != "" {
		t.Errorf("WrapText of blank = %q, want empty", got)
	}
}
