package runtime

import (
	"fmt"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	attentionpolicy "github.com/reliant-labs/reliant/internal/workflow/attention"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"go.temporal.io/sdk/workflow"
)

const attentionChangeID = "workflow-attention-v1"

type attentionContextKey struct{}

// AttentionHandoff preserves one actor over history-compaction continuations.
// Temporal's WorkflowStartTime is per execution (the current started event),
// but a recovery lease must remain bounded over the whole continuation chain.
// This is runtime state, not part of workflow inputs/YAML or an RPC.
type AttentionHandoff struct {
	Policy     *reliantv1.WorkflowAttention
	Key, RunID string
	StartedAt  time.Time
}

func attentionHandoff(ctx workflow.Context) *AttentionHandoff {
	a, _ := ctx.Value(attentionContextKey{}).(*runAttention)
	if a == nil || a.policy == nil {
		return nil
	}
	return &AttentionHandoff{Policy: a.policy, Key: a.key, RunID: a.runID, StartedAt: a.startedAt}
}

// runAttention is allocated before the completion defer: failures after policy
// evaluation carry exactly the same immutable actor identity as the start.
type runAttention struct {
	policy    *reliantv1.WorkflowAttention
	key       string
	runID     string
	startedAt time.Time
}

func attentionKeyForRun(policy *reliantv1.WorkflowAttention, inputs map[string]interface{}, handoff *AttentionHandoff) (string, error) {
	if handoff == nil {
		return resolveAttention(policy, inputs)
	}
	if handoff.RunID == "" || handoff.StartedAt.IsZero() || handoff.Policy == nil || strings.TrimSpace(handoff.Key) == "" || len(handoff.Key) > 512 {
		return "", fmt.Errorf("invalid attention continuation identity")
	}
	if err := attentionpolicy.Validate(handoff.Policy); err != nil {
		return "", err
	}
	return handoff.Key, nil
}

func resolveAttention(policy *reliantv1.WorkflowAttention, inputs map[string]interface{}) (string, error) {
	if err := attentionpolicy.Validate(policy); err != nil {
		return "", err
	}
	value, err := wfcel.EvaluateTemplate(policy.GetKey(), &wfcel.NodeResolutionContext{Inputs: inputs})
	if err != nil {
		return "", fmt.Errorf("attention.key: %w", err)
	}
	key, ok := value.(string)
	if !ok || strings.TrimSpace(key) == "" || len(key) > 512 {
		return "", fmt.Errorf("attention.key must evaluate to a non-empty string of at most 512 bytes")
	}
	return key, nil
}

func (a *runAttention) update(chatID, status, outcome, detail string) *core.AttentionUpdate {
	if a == nil || a.policy == nil {
		return nil
	}
	u := &core.AttentionUpdate{ChatID: chatID, RunID: a.runID, StartedAt: a.startedAt, Key: a.key}
	switch status {
	case "started":
		u.Phase, u.State = "start", a.policy.GetOnStart()
		if u.State == "recovering" {
			u.RecoveryTimeout, _ = time.ParseDuration(a.policy.GetRecoveryTimeout())
		}
	case "completed", "failed", "cancelled":
		u.Phase = "finish"
		if status == "completed" && outcome != model.OutcomeFailure {
			u.State = a.policy.GetOnSuccess()
			// Recovery ownership ends with the run, not the lease's remaining time.
			if u.State == "" && a.policy.GetOnStart() == "recovering" {
				u.State = "action_required"
			}
		} else {
			u.State = "action_required"
			u.FailureClass = "execution_error"
			if status == "completed" {
				u.FailureClass = "unsuccessful"
			}
			u.Detail = detail
		}
	default:
		return nil
	}
	return u
}

func attentionForStatus(ctx workflow.Context, chatID, status string, opts *workflowStatusOpts) *core.AttentionUpdate {
	a, _ := ctx.Value(attentionContextKey{}).(*runAttention)
	outcome, detail := "", ""
	if opts != nil {
		outcome, detail = opts.Outcome, opts.Error
	}
	return a.update(chatID, status, outcome, detail)
}
