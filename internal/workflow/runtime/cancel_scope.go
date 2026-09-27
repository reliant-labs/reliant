package runtime

import "go.temporal.io/sdk/workflow"

// CANCELLING SEVERAL CHILD CONTEXTS AT ONCE MUST HAPPEN IN A FIXED ORDER.
//
// The Temporal SDK keeps a cancellable context's children in a Go map and
// cancels them with `for child := range children`. Cancelling a context whose
// subtree is running activities or timers therefore emits the
// RequestCancelActivityTask / CancelTimer commands in map-iteration order,
// which Go randomizes. The original run records one order, a replay generates
// another, and the workflow task fails with TMPRL1100 forever.
//
// Seen on 2026-09-27: a pause landed while three threads each had a CallLLM
// in flight, every one under its own interrupt child context. The history
// recorded cancels for 2710, 2696, 2724; the replay after a worker restart
// generated 2724 first.
//
// A cancelScope makes the order ours. Every child created under it with
// withScopedCancel is recorded, and cancelAll cancels them explicitly, newest
// first, before cancelling the scope itself. A child cancelled explicitly
// removes itself from its parent's map, so by the time the scope's own cancel
// runs, the map holds nothing that issues a command. Newest first so a
// grandchild is always cancelled before the child whose map would otherwise
// cancel it.

// orderedCancelChangeID gates the ordered cancel with workflow.GetVersion.
// Histories recorded before it replay the old map-ordered cancel — the only
// thing they can replay against — and new runs record the ordered one.
const orderedCancelChangeID = "ordered-scope-cancel"

type cancelScopeKey struct{}

type cancelScope struct {
	root    workflow.Context
	entries []scopedCancel
}

type scopedCancel struct {
	ctx    workflow.Context
	cancel workflow.CancelFunc
}

// newCancelScope returns a cancellable context carrying a scope, and the
// function that cancels everything under it in a deterministic order.
func newCancelScope(parent workflow.Context) (workflow.Context, workflow.CancelFunc) {
	s := &cancelScope{root: parent}
	ctx, cancel := workflow.WithCancel(parent)
	ctx = workflow.WithValue(ctx, cancelScopeKey{}, s)
	return ctx, func() {
		if workflow.GetVersion(s.root, orderedCancelChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion {
			s.cancelChildren()
		}
		cancel()
	}
}

// withScopedCancel is workflow.WithCancel that records the child in the
// nearest enclosing cancelScope, if there is one. Use it for every cancellable
// context that may sit under the pause coordinator's activity context.
func withScopedCancel(parent workflow.Context) (workflow.Context, workflow.CancelFunc) {
	ctx, cancel := workflow.WithCancel(parent)
	if s, ok := parent.Value(cancelScopeKey{}).(*cancelScope); ok && s != nil {
		s.add(ctx, cancel)
	}
	return ctx, cancel
}

// add records a child, first dropping children already cancelled (by an
// interrupt, a finished timer) so the list tracks live contexts rather than
// every activity the scope ever saw.
func (s *cancelScope) add(ctx workflow.Context, cancel workflow.CancelFunc) {
	live := s.entries[:0]
	for _, e := range s.entries {
		if e.ctx.Err() == nil {
			live = append(live, e)
		}
	}
	s.entries = append(live, scopedCancel{ctx: ctx, cancel: cancel})
}

// cancelChildren cancels every recorded child, newest first.
func (s *cancelScope) cancelChildren() {
	for i := len(s.entries) - 1; i >= 0; i-- {
		s.entries[i].cancel()
	}
	s.entries = nil
}
