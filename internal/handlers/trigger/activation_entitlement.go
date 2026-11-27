// Copyright (c) 2026 Reliant Labs
package trigger

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
)

// activationEntitlementTimeout bounds asking the gate whether the activating
// owner is entitled to a feature; a slow answer hides the feature's tools.
const activationEntitlementTimeout = 5 * time.Second

// gatedToolHidden returns whether the approval card for user's activation
// must treat a tool as nonexistent: it is feature-gated and the owner is not
// entitled to its feature in the activation's org. A name that is not gated
// is answered from the gated map alone, so the gate (and orgOf) is asked only
// for a gated name the caller passes, and each feature at most once. It fails
// closed: no gate, no org, a refusal or an error hides the tool.
func (s *Service) gatedToolHidden(ctx context.Context, user string, orgOf func() string) func(name string) bool {
	answers := map[featuregate.Feature]bool{}
	org, orgResolved := "", false
	return func(name string) bool {
		f, gated := tools.FeatureGate(name)
		if !gated {
			return false
		}
		if entitled, asked := answers[f]; asked {
			return !entitled
		}
		entitled := false
		if s.deps.Gate != nil {
			if !orgResolved {
				org, orgResolved = orgOf(), true
			}
			ctx, cancel := context.WithTimeout(ctx, activationEntitlementTimeout)
			allowed, err := s.deps.Gate.Allowed(ctx, f, user, org)
			cancel()
			if err != nil {
				logging.WarnContext(ctx, "[trigger] could not establish whether the activating owner is entitled to a feature; its tools are not named",
					"user_id", user, "feature", string(f), "error", err)
			}
			entitled = err == nil && allowed
		}
		answers[f] = entitled
		return !entitled
	}
}
