package obsbackend

import "testing"

func TestWhyOfReadsTheControlPlanesPayloadAndNothingElse(t *testing.T) {
	if got := string(whyOf(map[string]string{"why": `{"detector":"release_regressed"}`, "summary": "s"})); got != `{"detector":"release_regressed"}` {
		t.Errorf("why = %q", got)
	}
	// A user's own `why` annotation is prose, not the payload: it is not passed off as one.
	for name, ann := range map[string]map[string]string{
		"none":    nil,
		"empty":   {"why": ""},
		"prose":   {"why": "because the deploy was bad"},
		"array":   {"why": `[1]`},
		"literal": {"why": `null`},
	} {
		if got := whyOf(ann); got != nil {
			t.Errorf("%s: why = %q, want none", name, got)
		}
	}
}
