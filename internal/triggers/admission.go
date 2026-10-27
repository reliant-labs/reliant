package triggers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// AdmissionStore is implemented by the real repository. Admission is fail
// closed when a concurrency declaration meets an unwired store.
type AdmissionStore interface {
	GetAdmissionEvent(context.Context, string) (*core.TriggerEvent, error)
	EnqueueTriggerAdmission(context.Context, *core.TriggerEvent, core.TriggerAdmissionRequest) (*core.TriggerEvent, bool, error)
	GetTriggerAdmission(context.Context, string) (*core.TriggerAdmission, error)
	ReserveTriggerAdmission(context.Context, string, int) error
	BindTriggerAdmissionRun(context.Context, string, string) error
	SettleOrphanQueuedAdmission(context.Context, string) error
	TriggerAdmissionHighWater(context.Context) (int64, error)
	ListTriggerAdmissions(context.Context, int64, int64, int) ([]core.TriggerAdmission, error)
	ReleaseTriggerAdmission(context.Context, string, string, string) (bool, error)
}

// AdmissionSubject is source-normalized observed identity, never proof that
// this delivery is the current provider head. No client payload sets it.
type AdmissionSubject struct{ Head string }

// admissionPendingLimit is server configuration, not a workflow policy.
func admissionPendingLimit() (int, error) {
	s := os.Getenv("RELIANT_ADMISSION_PENDING_LIMIT")
	if s == "" {
		return 1000, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid RELIANT_ADMISSION_PENDING_LIMIT")
	}
	return n, nil
}

func admissionScope(owner, project, workflow string) string {
	b, _ := json.Marshal([]string{owner, project, runevents.WorkflowRef(workflow)})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (in *Intake) enqueueAdmission(ctx context.Context, t *core.Trigger, decl *Declaration, ev InboundEvent, row *core.TriggerEvent) (*core.TriggerEvent, bool, error) {
	repo, ok := in.repo.(AdmissionStore)
	if !ok {
		return nil, false, fmt.Errorf("automation admission store is not wired")
	}
	scope := ActivationScope(ctx, in.repo, t, decl, FilterInput{Kind: string(ev.Kind), TriggerID: t.ID, Payload: ev.Payload, Sender: ev.Sender, Origin: row.Origin, OccurredAt: row.OccurredAt}.Root())
	key, err := triggerspec.ConcurrencyKey(decl.Concurrency.Key, scope)
	if err != nil {
		return nil, false, err
	}
	head := ""
	if ev.AdmissionSubject != nil {
		head = ev.AdmissionSubject.Head
	}
	if len(head) > 256 {
		return nil, false, fmt.Errorf("observed admission head exceeds 256 bytes")
	}
	limit, err := admissionPendingLimit()
	if err != nil {
		return nil, false, err
	}
	return repo.EnqueueTriggerAdmission(ctx, row, core.TriggerAdmissionRequest{Scope: admissionScope(t.UserID, t.ProjectID, decl.CanonicalWorkflow), Key: key, Head: head, PendingLimit: limit})
}

func readAdmission(ctx context.Context, repo any, id string) (*core.TriggerAdmission, error) {
	r, ok := repo.(AdmissionStore)
	if !ok {
		return nil, nil
	}
	a, err := r.GetTriggerAdmission(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}
