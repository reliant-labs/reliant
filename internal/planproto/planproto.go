// Copyright (c) 2025 Reliant Labs

// Package planproto maps the plan and task columns the store keeps as int32
// onto the proto enums the Plan, Task and Chat services return. The stored
// values match the enums' numbers, so each is a cast; the functions keep the
// mapping in one place.
//
//forge:exclude-contract: pure int32-to-enum mapping; no I/O, state or time
package planproto

import reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

// --- PlanStatus ---

// PlanStatus is the stored plan status as the proto enum.
func PlanStatus(v int32) reliantv1.PlanStatus {
	return reliantv1.PlanStatus(v)
}

// --- PlanComplexity ---

// PlanComplexity is the stored plan complexity as the proto enum.
func PlanComplexity(v int32) reliantv1.PlanComplexity {
	return reliantv1.PlanComplexity(v)
}

// PlanComplexityToInt32Ptr is the column value for an optional complexity:
// nil stays nil.
func PlanComplexityToInt32Ptr(e *reliantv1.PlanComplexity) *int32 {
	if e == nil {
		return nil
	}
	v := int32(*e)
	return &v
}

// --- TaskStatus ---

// TaskStatus is the stored task status as the proto enum.
func TaskStatus(v int32) reliantv1.TaskStatus {
	return reliantv1.TaskStatus(v)
}
