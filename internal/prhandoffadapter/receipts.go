// Copyright (c) 2025 Reliant Labs
package prhandoffadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/prhandoff"
)

// Receipts decorates ONLY authenticated PR creation. Installing it is a future
// explicit opt-in: no app provider currently constructs it. Wrap the existing
// ledger-decorated runner, rather than replacing the ledger or permissions.
type Receipts struct {
	Inner    writeledger.Runner
	Registry prhandoff.Service
	Authors  prhandoff.Authors
}

// RunAuthenticated resolves the actual activity caller before PR creation and
// enrolls only a successful provider receipt. A post-write failure is explicitly
// non-retryable: never blindly repeat a provider mutation to repair enrollment.
func (r Receipts) RunAuthenticated(ctx context.Context, m *reliantv1.IntegrationManifest, action *reliantv1.ActionSpec, params map[string]any, src httpaction.CredentialSource, site httpaction.CallSite) (*httpaction.Result, error) {
	if r.Inner == nil {
		return nil, fmt.Errorf("PR handoff runner missing")
	}
	if m.GetId() != "github" || action.GetId() != "pr.create" {
		return r.Inner.RunAuthenticated(ctx, m, action, params, src, site)
	}
	if r.Registry == nil || r.Authors == nil {
		return nil, fmt.Errorf("PR handoff registry missing")
	}
	// Resolve ChatID from the persisted root, never from PR/body/tool params.
	caller, err := r.Authors.Resolve(ctx, prhandoff.Site{RootID: site.RunID, ExecutionID: executionID(ctx)})
	if err != nil {
		return nil, err
	}
	trustedSite, err := ActivitySite(ctx, site.RunID, caller.ChatID)
	if err != nil {
		return nil, err
	}
	result, err := r.Inner.RunAuthenticated(ctx, m, action, params, src, site)
	if err != nil || result == nil || result.IsError {
		return result, err
	}
	owner, _ := params["owner"].(string)
	repo, _ := params["repo"].(string)
	sha, _ := result.Data["head_sha"].(string)
	enrollErr := r.Registry.Created(ctx, prhandoff.CreateInput{Site: trustedSite, Repository: owner + "/" + repo, Number: integer(result.Data["number"]), ObjectID: integer(result.Data["id"]), HeadSHA: sha})
	if enrollErr != nil {
		// Preserve the successful provider response so the caller can see the PR.
		// Reconciliation needs a provider GET + authenticated original receipt; it
		// is not an excuse to create another PR or infer authorship from prose.
		copy := *result
		copy.IsError, copy.Retryable = true, false
		copy.Content = "PR was created, but original-author handoff enrollment failed; needs human reconciliation, do not retry PR creation: " + enrollErr.Error()
		return &copy, nil
	}
	return result, nil
}

func integer(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		if n > 0 && n <= 9007199254740991 && n == float64(int64(n)) {
			return int64(n)
		}
	case json.Number:
		i, _ := strconv.ParseInt(string(n), 10, 64)
		return i
	}
	return 0
}
