// Copyright (c) 2025 Reliant Labs
package schema_test

import (
	"fmt"
	"strings"
	"testing"

	// Registers every builder-visible activity.
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// TestEveryBuilderFieldIsDocumented is the bar for what a node's config form
// shows. ListNodes serves exactly these fields; the editor prints each one's
// description under its input and uses its example as the placeholder. An
// empty box with only an Aa/{} toggle (Execute Tools' "Tool calls") says
// nothing about what goes in it.
//
// Every field the builder renders needs a description, and an example unless
// its widget already shows the value space: a toggle (boolean) or a dropdown
// (enum values).
func TestEveryBuilderFieldIsDocumented(t *testing.T) {
	var problems []string
	for _, node := range schema.ListVisibleActivities() {
		for _, f := range node.InputFields {
			where := fmt.Sprintf("%s.%s", node.ID, f.Name)
			if strings.TrimSpace(f.Description) == "" {
				problems = append(problems, where+": no description")
			}
			if strings.TrimSpace(f.Example) == "" && f.Type != "boolean" && len(f.EnumValues) == 0 {
				problems = append(problems, where+": no example")
			}
		}
	}
	if len(problems) > 0 {
		t.Errorf("%d undocumented builder fields (set description/example in the field's (reliant) annotation):\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}
}
