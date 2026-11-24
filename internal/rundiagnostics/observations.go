// Copyright (c) 2026 Reliant Labs
package rundiagnostics

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ObservationTextBudget bounds the attributes and events one get_run response
// carries (a stored row is at most 8 KiB, so the row limit alone would allow
// 1.6 MiB on a view that polls).
const ObservationTextBudget = 256 << 10

// Observations projects stored node observations for a run surface, in the
// order given, until their attributes and events exceed budget bytes;
// truncated reports that rows were left out. The rows were redacted and
// bounded when they were written (internal/workflow/observe), so nothing is
// re-derived here.
func Observations(rows []*core.NodeObservation, budget int) (out []*pb.NodeObservation, truncated bool) {
	out = make([]*pb.NodeObservation, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		size := len(row.Attributes) + len(row.Events) + len(row.Errors)
		if size > budget {
			return out, true
		}
		budget -= size
		out = append(out, Observation(row))
	}
	return out, false
}

// Observation projects one stored observation. A column that does not read
// back as the JSON the writer stores is left empty and named in errors, so a
// damaged row still says which node it was.
func Observation(row *core.NodeObservation) *pb.NodeObservation {
	o := &pb.NodeObservation{
		NodeId:     row.StepID,
		NodePath:   row.NodePath,
		WorkflowId: row.WorkflowID,
		Attempt:    row.Attempt,
		Source:     row.Source,
		Truncated:  row.Truncated,
	}
	if !row.CreatedAt.IsZero() {
		o.CreatedAt = row.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if row.LoopNodeID != nil {
		o.LoopNodeId = *row.LoopNodeID
	}
	if row.LoopIteration != nil {
		iteration := int32(*row.LoopIteration)
		o.LoopIteration = &iteration
	}
	if row.Errors != "" {
		if err := json.Unmarshal([]byte(row.Errors), &o.Errors); err != nil {
			o.Errors = []string{"the recorded errors could not be read"}
		}
	}
	if attrs, ok := structOf(row.Attributes); ok {
		o.Attributes = attrs
	} else {
		o.Errors = append(o.Errors, "the recorded attributes could not be read")
	}
	if row.Events != "" {
		var events []struct {
			Name       string                 `json:"name"`
			Attributes map[string]interface{} `json:"attributes"`
		}
		if err := json.Unmarshal([]byte(row.Events), &events); err != nil {
			o.Errors = append(o.Errors, "the recorded events could not be read")
		}
		for _, e := range events {
			ev := &pb.ObservedEvent{Name: e.Name}
			if len(e.Attributes) > 0 {
				s, err := structpb.NewStruct(e.Attributes)
				if err != nil {
					o.Errors = append(o.Errors, "an event's attributes could not be read")
					continue
				}
				ev.Attributes = s
			}
			o.Events = append(o.Events, ev)
		}
	}
	return o
}

// structOf reads a stored attributes object; "" is no attributes.
func structOf(text string) (*structpb.Struct, bool) {
	if text == "" {
		return nil, true
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return nil, false
	}
	if len(m) == 0 {
		return nil, true
	}
	s, err := structpb.NewStruct(m)
	if err != nil {
		return nil, false
	}
	return s, true
}
