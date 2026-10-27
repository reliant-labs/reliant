package triggers

import (
	"context"
	"fmt"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
)

// AdmissionDescriber is execution truth, not the DB's terminal-status cache.
type AdmissionDescriber interface {
	DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error)
}

func (r *Redriver) redriveAdmissions(ctx context.Context) error {
	repo, ok := r.repo.(AdmissionStore)
	if !ok {
		return nil
	}
	temporal, ok := r.starter.(AdmissionDescriber)
	if !ok {
		return fmt.Errorf("admission Temporal describer is not wired")
	}
	if r.admissionThrough == 0 {
		high, err := repo.TriggerAdmissionHighWater(ctx)
		if err != nil {
			return err
		}
		r.admissionThrough = high
	}
	batch := r.admissionBatch
	if batch <= 0 {
		batch = 2000
	}
	rows, err := repo.ListTriggerAdmissions(ctx, r.admissionAfter, r.admissionThrough, batch)
	if err != nil {
		return err
	}
	// Freeze each sweep's upper bound. Continuous intake cannot postpone
	// revisiting old reservations forever. Replacements with older ordinals
	// are revisited on the next sweep too.
	defer func() {
		if len(rows) < batch || r.admissionAfter >= r.admissionThrough {
			r.admissionAfter, r.admissionThrough = 0, 0
		}
	}()
	if len(rows) == 0 {
		return nil
	}
	for _, a := range rows {
		r.admissionAfter = a.Order
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if a.State == "reserved" && a.ChatID != "" {
			// Unknown run ID is recoverable ONLY with the immutable event memo.
			// NotFound is ambiguous and never frees a permit.
			desc, e := temporal.DescribeWorkflowExecution(ctx, a.ChatID, a.RunID)
			if e != nil {
				if a.RunID == "" {
					// Retry the recorded launch. Launcher only resumes its PENDING
					// root; an ACTIVE/reused root is never blindly restarted.
					if err := r.startAdmissionFire(ctx, repo, a); err != nil {
						return err
					}
				}
				continue
			}
			if a.RunID == "" {
				runID, matched := admissionMemoRun(desc, a)
				if !matched {
					continue
				}
				if err = repo.BindTriggerAdmissionRun(ctx, a.EventID, runID); err != nil {
					return err
				}
				a.RunID = runID
			}
			info := desc.GetWorkflowExecutionInfo()
			if info.GetExecution().GetWorkflowId() != a.ChatID || info.GetExecution().GetRunId() != a.RunID {
				continue
			}
			if r.continuity == nil {
				continue
			}
			// Resolve from the immutable ORIGINAL admission execution, never the
			// chat's current pointer. A CAN close is an edge, not logical closure.
			logical, proofErr := runcontinuity.Resolve(ctx, r.continuity, runcontinuity.Execution{WorkflowID: a.ChatID, RunID: a.RunID})
			if proofErr != nil {
				continue
			} // unknown/cycle/retention/64-hop bound: hold
			if !logical.Live && admissionTerminal(logical.Terminal) {
				// CAS still names the original reservation, not its successor.
				if _, err = repo.ReleaseTriggerAdmission(ctx, a.EventID, a.ChatID, a.RunID); err != nil {
					return err
				}
			}
			// A known running/closed execution is never started again.
			continue
		}
		if err := r.startAdmissionFire(ctx, repo, a); err != nil {
			return err
		}
	}
	return nil
}

func (r *Redriver) startAdmissionFire(ctx context.Context, repo AdmissionStore, a core.TriggerAdmission) error {
	event, err := repo.GetAdmissionEvent(ctx, a.EventID)
	if err != nil {
		return err
	}
	if event.TriggerID == nil {
		// Trigger deletion uses ON DELETE SET NULL. A never-admitted orphan
		// must leave FIFO eligibility; a reservation still belongs to its
		// exact Temporal execution and is never released here.
		if a.State == "queued" {
			err = repo.SettleOrphanQueuedAdmission(ctx, event.ID)
		}
		return err
	}
	return StartEventFire(ctx, r.starter, event.ID, EventFireInput{TriggerID: *event.TriggerID, Kind: event.Kind, DedupeKey: event.DedupeKey}, r.taskQueue)
}

func admissionMemoRun(desc *workflowservice.DescribeWorkflowExecutionResponse, a core.TriggerAdmission) (string, bool) {
	info := desc.GetWorkflowExecutionInfo()
	if info.GetExecution().GetWorkflowId() != a.ChatID {
		return "", false
	}
	var eventID string
	p := info.GetMemo().GetFields()["reliant_admission_event_id"]
	if p == nil || converter.GetDefaultDataConverter().FromPayload(p, &eventID) != nil || eventID != a.EventID {
		return "", false
	}
	runID := info.GetExecution().GetRunId()
	return runID, runID != ""
}

func admissionTerminal(status enumspb.WorkflowExecutionStatus) bool {
	switch status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
		enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED,
		enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
		return true
	default:
		return false // RUNNING, unknown, and continue-as-new chains retain capacity.
	}
}
