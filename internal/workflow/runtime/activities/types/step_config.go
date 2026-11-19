// Copyright (c) 2025 Reliant Labs
package types

// StepConfigFailedErrType is the Temporal error type of a step whose own
// config cannot run: decided by the definition and the data it reads, so no
// retry or resume can change it. The workflow ends the run with it rather than
// pausing (the runtime's endsRunInsteadOfPausing), so the run's on_failure
// handler runs and an automation's capacity slot is freed. An activity returns
// it, non-retryable and in the Benign category, for a node it can tell will
// never run — an invoke_tool node naming a tool that is not exposed as a node.
const StepConfigFailedErrType = "StepConfigFailed"
