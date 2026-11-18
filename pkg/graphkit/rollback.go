package graphkit

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// BuildWithRollback is Build with construction-failure resource ownership.
// It is opt-in; Build retains its existing behavior. ctx is checked before and
// after force (constructors must honor it themselves). timeout must be positive.
// On failure, every successfully constructed node's Close runs in reverse
// completion order, using one fresh background context bounded by timeout.
// Close must honor that context, just as it must during normal supervision.
// Run and Drain are not called: these resources have not started serving.
//
// The original build/cancellation error and all cleanup errors remain reachable
// through errors.Is/As. A failed graph is terminal: its memoized values may now
// be closed, so neither access, rebuilding nor supervision is permitted.
// On success ownership transfers to Supervise; construction rollback never
// closes a successfully built graph. A constructor returning an error still
// owns its own partial value; only successful Get acquisitions are registered.
// Like Build, this method and construction use one goroutine.
func (b *Builder) BuildWithRollback(ctx context.Context, root string, timeout time.Duration, force func()) error {
	if timeout <= 0 {
		return errors.New("graphkit: construction rollback timeout must be positive")
	}
	if b.sealed || b.rolledBack {
		return fmt.Errorf("build %s: graph already built or rolled back", root)
	}
	b.rollbackManaged = true
	err := ctx.Err()
	if err == nil {
		// Build preserves legacy panic behavior. This opt-in boundary turns
		// an unexpected force panic into an error so acquisitions still unwind.
		err = func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					if cause, ok := p.(error); ok {
						err = fmt.Errorf("build %s panicked: %w", root, cause)
					} else {
						err = fmt.Errorf("build %s panicked: %v", root, p)
					}
				}
			}()
			return b.Build(root, force)
		}()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		return nil
	}
	return errors.Join(err, b.Rollback(timeout))
}

// Rollback releases acquisitions before supervision, including when a resource
// outside the graph (for example a probe listener) fails after Build succeeded.
// It is idempotent and terminal. After Supervise takes ownership it is a no-op,
// so a deferred rollback cannot double-close resources on normal shutdown.
// The same cooperative timeout contract as BuildWithRollback applies.
func (b *Builder) Rollback(timeout time.Duration) error {
	if timeout <= 0 {
		return errors.New("graphkit: construction rollback timeout must be positive")
	}
	if b.rolledBack || b.supervised {
		return nil
	}
	b.rolledBack = true // set before invoking user cleanup; never retry a close
	cleanupCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var errs []error
	for i := len(b.built) - 1; i >= 0; i-- {
		n := b.built[i]
		if n.node.Close != nil {
			if closeErr := closeAcquisition(cleanupCtx, n.node.Close); closeErr != nil {
				errs = append(errs, fmt.Errorf("rollback %s: %w", n.name, closeErr))
			}
		}
	}
	return errors.Join(errs...)
}

func closeAcquisition(ctx context.Context, close func(context.Context) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("close panicked: %v", p)
		}
	}()
	return close(ctx)
}
