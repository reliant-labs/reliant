// Package violating is a deliberate violation for the serializer guard's
// negative proof. It sits in testdata, so the go tool never builds it; the
// guard loads it by directory.
package violating

import (
	"encoding/xml"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/reliant/internal/db"
)

type payload struct {
	Rows []*db.StepExecution
}

// A model inside a struct field.
func InStruct(rows []*db.StepExecution) ([]byte, error) {
	return xml.Marshal(payload{Rows: rows})
}

// A model inside a map literal: the static type is map[string]any.
func InMapLiteral(s *db.WorkflowScenario) ([]byte, error) {
	return yaml.Marshal(map[string]any{"scenario": s})
}

// A model behind a call the guard cannot see into.
func InWrapper(rows []*db.ChatStepExecution) ([]byte, error) {
	return xml.Marshal(unknown.Wrap(rows))
}
