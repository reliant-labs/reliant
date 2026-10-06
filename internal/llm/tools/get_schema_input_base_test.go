// Copyright (c) 2025 Reliant Labs
package tools

import (
	"strings"
	"testing"
)

// An agent writing a workflow learns an input's fields from get_schema. The
// fields every input shares (description, ui, example) are listed from the
// proto descriptor, which in a built binary carries no source comments, so
// their rows used to be blank. `example` in particular must say it is not a
// default, or an agent fills it in where `default` belongs.
func TestGetSchemaDescribesTheFieldsEveryInputShares(t *testing.T) {
	doc, err := getInputTypeDoc("string")
	if err != nil {
		t.Fatalf("get_schema string: %v", err)
	}
	for field, want := range map[string]string{
		"description": "Run and activation forms show it",
		"ui":          "toolbar",
		"example":     "Never used as a value",
	} {
		row := rowFor(doc, field)
		if row == "" {
			t.Errorf("get_schema string lists no %q field:\n%s", field, doc)
			continue
		}
		if !strings.Contains(row, want) {
			t.Errorf("the %q row does not say %q: %s", field, want, row)
		}
	}
}

// rowFor returns the markdown table row documenting field, or "".
func rowFor(doc, field string) string {
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "| `"+field+"` |") {
			return line
		}
	}
	return ""
}
