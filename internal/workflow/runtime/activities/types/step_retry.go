// Copyright (c) 2025 Reliant Labs
package types

// StepActivityMaxAttempts is the length of the retry ladder every graph step
// is dispatched with (the runtime's stepActivityRetryPolicy). It is here, not
// in the runtime, so an activity that schedules its own retries can tell its
// last attempt even when Temporal does not send the policy
// (ActivityInfo.RetryPolicy is nil-able, and nil in practice): the action
// node's rate-limit wait hands the graph today's output on that attempt rather
// than a failure that would pause the run.
const StepActivityMaxAttempts int32 = 5
