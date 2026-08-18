package runtime

import "go.temporal.io/sdk/workflow"

const InterruptThreadSignalName = "interrupt_thread"

type InterruptThreadSignal struct {
	ThreadID string `json:"thread_id,omitempty"`
	Thread   string `json:"thread,omitempty"`
	Epoch    int64  `json:"epoch,omitempty"`
}

type ThreadInterruptCoordinator struct {
	workflowID string
	states     map[string]*threadInterruptState
}

type threadInterruptState struct {
	epoch   int64
	waiters []threadInterruptActivityContext
}

type threadInterruptActivityContext struct {
	epoch  int64
	cancel func()
}

type ThreadInterrupt struct {
	coordinator *ThreadInterruptCoordinator
	thread      string
}

func NewThreadInterruptCoordinator(ctx workflow.Context, workflowID string) *ThreadInterruptCoordinator {
	coordinator := &ThreadInterruptCoordinator{
		workflowID: workflowID,
		states:     make(map[string]*threadInterruptState),
	}

	signalCh := workflow.GetSignalChannel(ctx, InterruptThreadSignalName)
	logger := workflow.GetLogger(ctx)
	workflow.Go(ctx, func(gCtx workflow.Context) {
		for {
			if gCtx.Err() != nil {
				return
			}
			var signal InterruptThreadSignal
			if !signalCh.Receive(gCtx, &signal) {
				return
			}
			thread := signal.ThreadID
			if thread == "" {
				thread = signal.Thread
			}
			if coordinator.interrupt(thread, signal.Epoch) {
				logger.Info("[ThreadInterrupt] Thread interrupt recorded",
					"workflowID", workflowID,
					"thread", thread,
					"epoch", coordinator.epoch(thread),
				)
			}
		}
	})

	return coordinator
}

func (c *ThreadInterruptCoordinator) ForThread(thread string) *ThreadInterrupt {
	if c == nil || thread == "" {
		return nil
	}
	c.state(thread)
	return &ThreadInterrupt{coordinator: c, thread: thread}
}

func (c *ThreadInterruptCoordinator) state(thread string) *threadInterruptState {
	if c.states == nil {
		c.states = make(map[string]*threadInterruptState)
	}
	state := c.states[thread]
	if state == nil {
		state = &threadInterruptState{}
		c.states[thread] = state
	}
	return state
}

func (c *ThreadInterruptCoordinator) epoch(thread string) int64 {
	if c == nil || thread == "" {
		return 0
	}
	state := c.states[thread]
	if state == nil {
		return 0
	}
	return state.epoch
}

func (c *ThreadInterruptCoordinator) interrupt(thread string, requestedEpoch int64) bool {
	if c == nil || thread == "" {
		return false
	}
	state := c.state(thread)

	nextEpoch := requestedEpoch
	if nextEpoch <= 0 {
		nextEpoch = state.epoch + 1
	}
	if nextEpoch <= state.epoch {
		return false
	}

	state.epoch = nextEpoch
	kept := state.waiters[:0]
	for _, waiter := range state.waiters {
		if waiter.epoch < state.epoch {
			waiter.cancel()
			continue
		}
		kept = append(kept, waiter)
	}
	state.waiters = kept
	return true
}

func (c *ThreadInterruptCoordinator) activityContext(thread string, base workflow.Context) workflow.Context {
	if c == nil || thread == "" {
		return base
	}
	state := c.state(thread)
	ctx, cancel := workflow.WithCancel(base)
	state.waiters = append(state.waiters, threadInterruptActivityContext{
		epoch:  state.epoch,
		cancel: cancel,
	})
	return ctx
}

func resolveThreadInterrupt(factory func(string) *ThreadInterrupt, fallback *ThreadInterrupt, thread string) *ThreadInterrupt {
	if factory != nil && thread != "" {
		return factory(thread)
	}
	if fallback == nil {
		return nil
	}
	if thread == "" || fallback.thread == "" || fallback.thread == thread {
		return fallback
	}
	return nil
}

func (h *ThreadInterrupt) Epoch() int64 {
	if h == nil || h.coordinator == nil || h.thread == "" {
		return 0
	}
	return h.coordinator.epoch(h.thread)
}

func (h *ThreadInterrupt) InterruptedSince(epoch int64) bool {
	return h.Epoch() > epoch
}

func (h *ThreadInterrupt) ActivityContext(base workflow.Context) workflow.Context {
	if h == nil || h.coordinator == nil || h.thread == "" {
		return base
	}
	return h.coordinator.activityContext(h.thread, base)
}
