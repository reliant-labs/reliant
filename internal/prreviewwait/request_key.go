// Copyright (c) 2025 Reliant Labs
package prreviewwait

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
)

// RequestKey is a deterministic opaque exact-execution/node-occurrence identity.
// It is recomputed by the server activity from actual Temporal execution; it
// cannot authorize a caller-selected subscription or another author's wait.
func RequestKey(execution runcontinuity.Execution, in NodeInput) string {
	raw, _ := json.Marshal([]any{execution.WorkflowID, execution.RunID, in.Occurrence, in.Repo, in.PR, in.HeadSHA})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
