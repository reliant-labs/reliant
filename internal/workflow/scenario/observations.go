// Copyright (c) 2026 Reliant Labs
package scenario

import (
	"fmt"
	"sort"
	"strings"
)

// checkObservationExpectations evaluates `expect.observations` against what
// the run's observe: blocks recorded. The runner evaluates every block
// through the runtime's own path, so recorded is the complete record.
func checkObservationExpectations(expect map[string]ObservationExpectation, recorded []RecordedObservation) []string {
	if len(expect) == 0 {
		return nil
	}
	byNode := map[string][]RecordedObservation{}
	for _, o := range recorded {
		byNode[o.Node] = append(byNode[o.Node], o)
	}
	nodes := make([]string, 0, len(expect))
	for n := range expect {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	var mismatches []string
	for _, node := range nodes {
		want := expect[node]
		got := byNode[node]
		var records []RecordedObservation
		for _, o := range got {
			if len(o.Attributes) > 0 || len(o.Events) > 0 {
				records = append(records, o)
			}
		}
		if want.Recorded != nil {
			if *want.Recorded && len(records) == 0 {
				mismatches = append(mismatches, fmt.Sprintf("expected node %q to record an observation, but it recorded none%s", node, observationErrorsNote(got)))
			}
			if !*want.Recorded && len(records) > 0 {
				mismatches = append(mismatches, fmt.Sprintf("expected node %q to record no observation, but it recorded %s", node, describeObservation(records[0])))
			}
		}
		if len(want.Attributes) > 0 && !anyObservation(records, func(o RecordedObservation) bool { return attributesMatch(want.Attributes, o.Attributes) }) {
			mismatches = append(mismatches, fmt.Sprintf("node %q recorded no observation with attributes %v (recorded: %s)%s", node, want.Attributes, describeObservations(records), observationErrorsNote(got)))
		}
		for _, ev := range want.Events {
			matched := anyObservation(records, func(o RecordedObservation) bool {
				for _, e := range o.Events {
					if e.Name == ev.Name && attributesMatch(ev.Attributes, e.Attributes) {
						return true
					}
				}
				return false
			})
			if !matched {
				mismatches = append(mismatches, fmt.Sprintf("node %q recorded no %q event with attributes %v (recorded: %s)%s", node, ev.Name, ev.Attributes, describeObservations(records), observationErrorsNote(got)))
			}
		}
		if want.NoErrors {
			for _, o := range got {
				if len(o.Errors) > 0 {
					mismatches = append(mismatches, fmt.Sprintf("node %q's observe block had expressions that failed: %s", node, strings.Join(o.Errors, "; ")))
					break
				}
			}
		}
	}
	return mismatches
}

func anyObservation(obs []RecordedObservation, match func(RecordedObservation) bool) bool {
	for _, o := range obs {
		if match(o) {
			return true
		}
	}
	return false
}

// attributesMatch reports whether every expected attribute is among got, by
// value (numbers by value, whatever their type).
func attributesMatch(want, got map[string]interface{}) bool {
	for k, v := range want {
		actual, ok := got[k]
		if !ok || !deepEqual(v, actual) {
			return false
		}
	}
	return true
}

func describeObservations(obs []RecordedObservation) string {
	if len(obs) == 0 {
		return "none"
	}
	parts := make([]string, len(obs))
	for i, o := range obs {
		parts[i] = describeObservation(o)
	}
	return strings.Join(parts, "; ")
}

func describeObservation(o RecordedObservation) string {
	var parts []string
	if len(o.Attributes) > 0 {
		parts = append(parts, fmt.Sprintf("attributes %v", o.Attributes))
	}
	for _, e := range o.Events {
		parts = append(parts, fmt.Sprintf("event %s %v", e.Name, e.Attributes))
	}
	return strings.Join(parts, ", ")
}

// observationErrorsNote names the expressions that failed, which is usually
// why an expected recording is missing.
func observationErrorsNote(obs []RecordedObservation) string {
	var errs []string
	for _, o := range obs {
		errs = append(errs, o.Errors...)
	}
	if len(errs) == 0 {
		return ""
	}
	return "; expressions that failed: " + strings.Join(errs, "; ")
}
