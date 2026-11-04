package wfyaml

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/version"
)

// A field this build does not define is reported as an UnknownFieldError
// naming the field and this build's version, at every level the parser
// checks keys: a file written for a newer Reliant and synced to an older
// server must say why it does not load, not fail as a bare parse error.
func TestUnknownFieldNamesFieldAndVersion(t *testing.T) {
	cases := map[string]struct {
		yaml  string
		field string
	}{
		"top level": {yaml: `
name: w
from_the_future: 1
nodes: [{id: a, type: call_llm}]
`, field: "from_the_future"},
		"trigger": {yaml: `
name: w
triggers:
  - name: nightly
    schedule: {cron: "0 0 * * *"}
    from_the_future: 1
nodes: [{id: a, type: call_llm}]
`, field: "from_the_future"},
		"trigger source": {yaml: `
name: w
triggers:
  - name: nightly
    schedule: {cron: "0 0 * * *", from_the_future: 1}
nodes: [{id: a, type: call_llm}]
`, field: "from_the_future"},
		"node": {yaml: `
name: w
nodes:
  - id: a
    type: call_llm
    from_the_future: 1
`, field: "from_the_future"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseWorkflow([]byte(tc.yaml))
			if err == nil {
				t.Fatal("ParseWorkflow accepted an unknown field")
			}
			var unknown *UnknownFieldError
			if !errors.As(err, &unknown) {
				t.Fatalf("error is not an UnknownFieldError: %v", err)
			}
			if unknown.Field != tc.field {
				t.Errorf("Field = %q, want %q", unknown.Field, tc.field)
			}
			for _, want := range []string{tc.field, "reliant " + version.Version, "upgrade the server"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// Any other parse failure is not dressed up as version skew.
func TestOtherParseErrorsCarryNoSkewHint(t *testing.T) {
	_, err := ParseWorkflow([]byte("name: w\nhidden: [not, a, bool]\n"))
	if err == nil {
		t.Fatal("ParseWorkflow accepted a list for hidden")
	}
	var unknown *UnknownFieldError
	if errors.As(err, &unknown) || strings.Contains(err.Error(), "upgrade the server") {
		t.Fatalf("a type error read as an unknown field: %v", err)
	}
}
