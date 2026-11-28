package triggers

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

var errCapacityUnwired = errors.New("test: capacity store not wired")

// unwiredCapacity is a CapacityStore whose every method fails loudly. A fake
// embeds it so it satisfies the required role, and a test that reaches
// capacity without wiring the method it needs fails instead of passing on a
// silent no-op.
type unwiredCapacity struct{}

func (unwiredCapacity) EnqueueTriggerCapacitySuperseding(context.Context, *core.TriggerEvent, store.TriggerCapacityScope) ([]*core.TriggerEvent, error) {
	return nil, errCapacityUnwired
}
func (unwiredCapacity) CanLaunchTriggerCapacity(context.Context, string, store.TriggerCapacityScope, store.TriggerCapacityLimits) (bool, error) {
	return false, errCapacityUnwired
}
func (unwiredCapacity) NextAdmissibleTriggerCapacityEvent(context.Context, store.TriggerCapacityScope, store.TriggerCapacityLimits) (*core.TriggerEvent, error) {
	return nil, errCapacityUnwired
}
func (unwiredCapacity) BindTriggerCapacityExecution(context.Context, string, string, string) error {
	return errCapacityUnwired
}
func (unwiredCapacity) PrepareTriggerCapacityExecution(context.Context, string, string) error {
	return errCapacityUnwired
}
func (unwiredCapacity) ListTriggerCapacityExecutions(context.Context, store.TriggerCapacityScope) ([]store.TriggerCapacityExecution, error) {
	return nil, errCapacityUnwired
}
func (unwiredCapacity) ListTriggerCapacityExecutionBatch(context.Context, int) ([]store.TriggerCapacityExecution, error) {
	return nil, errCapacityUnwired
}
func (unwiredCapacity) ReleaseTriggerCapacityExecution(context.Context, store.TriggerCapacityExecution) error {
	return errCapacityUnwired
}
func (unwiredCapacity) ReleaseClosedTriggerCapacityExecution(context.Context, store.TriggerCapacityExecution) error {
	return errCapacityUnwired
}
func (unwiredCapacity) FenceUnboundTriggerCapacityStart(context.Context, store.TriggerCapacityExecution) (bool, error) {
	return false, errCapacityUnwired
}
func (unwiredCapacity) ReleaseUnboundTriggerCapacity(context.Context, store.TriggerCapacityExecution, string) (bool, error) {
	return false, errCapacityUnwired
}
func (unwiredCapacity) TriggerCapacityStartFenced(context.Context, store.TriggerCapacityExecution) (bool, error) {
	return false, errCapacityUnwired
}

func (unwiredCapacity) ListTriggerCapacityKeyRuns(context.Context, store.TriggerCapacityScope, string, int64) ([]store.TriggerCapacityKeyRun, error) {
	return nil, errCapacityUnwired
}
func (unwiredCapacity) MarkTriggerCapacityRunSuperseded(context.Context, string, string) (bool, error) {
	return false, errCapacityUnwired
}
func (unwiredCapacity) SettleSupersededTriggerCapacityRun(context.Context, string) error {
	return errCapacityUnwired
}

// Expiry is the one quiet method: a redrive pass over a fake with no capacity rows has nothing to expire.
func (unwiredCapacity) ExpirePendingTriggerCapacityEvents(context.Context, time.Time, string, int) ([]*core.TriggerEvent, error) {
	return nil, nil
}

// idleCapacity is a CapacityStore with no occupied slots and nothing to
// expire, for redrive tests that exercise the delivery path.
type idleCapacity struct{ unwiredCapacity }

func (idleCapacity) ListTriggerCapacityExecutionBatch(context.Context, int) ([]store.TriggerCapacityExecution, error) {
	return nil, nil
}

var errReceiptUnwired = errors.New("test: receipt store not wired")

// unwiredReceipts is a ReceiptStore whose every method fails loudly; a fake
// that never takes a delivery receipt embeds it to satisfy IntakeRepo.
type unwiredReceipts struct{}

func (unwiredReceipts) TriggerDeliveryEvent(context.Context, string, string, string) (*core.TriggerEvent, error) {
	return nil, errReceiptUnwired
}
func (unwiredReceipts) RecordDeliveryTriggerEvent(context.Context, string, string, *core.TriggerEvent) (*core.TriggerEvent, bool, error) {
	return nil, false, errReceiptUnwired
}
