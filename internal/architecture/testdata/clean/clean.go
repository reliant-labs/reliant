// Package clean calls serializers with values that carry no model: the guard
// must count the calls and report no violation.
package clean

import (
	"encoding/xml"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/reliant/internal/db"
)

type summary struct{ Count int }

func Count(rows []*db.StepExecution) ([]byte, error) {
	return xml.Marshal(summary{Count: len(rows)})
}

func Counted(rows []*db.StepExecution) ([]byte, error) {
	return yaml.Marshal(map[string]any{"count": len(rows)})
}
