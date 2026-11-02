package serverkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// awaitDone fails the test unless ctx is done within a second.
func awaitDone(t *testing.T, ctx context.Context, what string) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatalf("%s: the bound context was not ended", what)
	}
}

func TestStreams_BeginDrainEndsUnclaimedStreamsWithTheDrainCause(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	ctx, release := streams.Bind(context.Background())
	defer release()

	if ctx.Err() != nil || serverkit.Ended(ctx) {
		t.Fatal("a bound context must stay live until the drain")
	}
	if ended, left := streams.BeginDrain(); ended != 1 || left != 0 {
		t.Fatalf("BeginDrain = (%d ended, %d left), want (1, 0)", ended, left)
	}
	awaitDone(t, ctx, "BeginDrain")
	if !serverkit.Ended(ctx) {
		t.Fatalf("Ended must report a drain; cause = %v", context.Cause(ctx))
	}
	if !svcerr.IsServerDraining(context.Cause(ctx)) {
		t.Fatalf("cause = %v, want svcerr.ErrServerDraining", context.Cause(ctx))
	}

	// A context derived inside the handler reports it too: that is how a
	// handler several calls deep learns why it was cancelled.
	derived, cancel := context.WithCancel(ctx)
	defer cancel()
	if !serverkit.Ended(derived) {
		t.Fatal("a context derived from a drained one must report the drain")
	}
}

// A stream that asked for the soft signal gets it, keeps running, and is
// ended only by the hard phase.
func TestStreams_ClaimedStreamGetsTheSoftSignalAndRunsUntilEnd(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	ctx, release := streams.Bind(context.Background())
	defer release()
	soft := serverkit.Draining(ctx)

	select {
	case <-soft:
		t.Fatal("the soft signal fired before the drain")
	default:
	}
	if ended, left := streams.BeginDrain(); ended != 0 || left != 1 {
		t.Fatalf("BeginDrain = (%d ended, %d left), want (0, 1)", ended, left)
	}
	select {
	case <-soft:
	case <-time.After(time.Second):
		t.Fatal("BeginDrain did not close the claimed stream's Draining channel")
	}
	if ctx.Err() != nil {
		t.Fatal("the soft phase must leave a claimed stream running")
	}
	if n := streams.End(); n != 1 {
		t.Fatalf("End ended %d streams, want 1", n)
	}
	awaitDone(t, ctx, "End")
	if !serverkit.Ended(ctx) {
		t.Fatal("the hard phase must end a claimed stream with the drain cause")
	}
}

func TestStreams_BoundAfterTheDrainBeganAreAlreadyEnded(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	streams.BeginDrain()
	streams.BeginDrain() // idempotent

	ctx, release := streams.Bind(context.Background())
	defer release()
	if !serverkit.Ended(ctx) {
		t.Fatal("a stream that arrives while the server drains must end at once")
	}
	if streams.Active() != 0 {
		t.Fatal("a stream ended at bind time is not left registered")
	}
}

func TestStreams_EndedIsFalseForOtherCancellations(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()

	parent, cancelParent := context.WithCancel(context.Background())
	clientGone, release := streams.Bind(parent)
	defer release()
	cancelParent()
	<-clientGone.Done()
	if serverkit.Ended(clientGone) {
		t.Fatal("the client going away is not a drain")
	}

	released, releaseIt := streams.Bind(context.Background())
	releaseIt()
	<-released.Done()
	if serverkit.Ended(released) {
		t.Fatal("a handler finishing on its own is not a drain")
	}
	streams.End()
	if serverkit.Ended(released) {
		t.Fatal("the drain must not rewrite the cause of a stream that had already finished")
	}
	if serverkit.Ended(context.Background()) {
		t.Fatal("a live context is not ended")
	}
	if serverkit.Draining(context.Background()) != nil {
		t.Fatal("a context with no registry has no drain to signal")
	}
}

// Active is what the drain reports and waits on, so a release must count
// exactly once however often it is called.
func TestStreams_ActiveCountsUnreleasedBindings(t *testing.T) {
	t.Parallel()
	var streams serverkit.Streams // the zero value is usable
	_, releaseA := streams.Bind(context.Background())
	_, releaseB := streams.Bind(context.Background())
	if got := streams.Active(); got != 2 {
		t.Fatalf("Active = %d, want 2", got)
	}
	releaseA()
	releaseA()
	if got := streams.Active(); got != 1 {
		t.Fatalf("Active after a double release = %d, want 1", got)
	}
	streams.End()
	releaseB()
	if got := streams.Active(); got != 0 {
		t.Fatalf("Active = %d, want 0", got)
	}
	if !streams.Wait(context.Background()) {
		t.Fatal("Wait on an empty registry must return at once")
	}
}

// Drain keeps DrainReserve of the budget back: a claimed stream that never
// returns on its own is ended at deadline − reserve, not at the deadline.
func TestStreams_DrainEndsHeldStreamsAtTheReserve(t *testing.T) {
	t.Parallel()
	streams := &serverkit.Streams{DrainReserve: time.Second}
	ctx, release := streams.Bind(context.Background())
	_ = serverkit.Draining(ctx) // claim the soft phase, then ignore it
	go func() {
		<-ctx.Done()
		release()
	}()

	budget, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := streams.Drain(budget); err != nil {
		t.Fatalf("Drain = %v, want nil: the stream returned once it was ended", err)
	}
	elapsed := time.Since(start)
	if elapsed < 1900*time.Millisecond || elapsed >= 3*time.Second {
		t.Fatalf("Drain ended the held stream after %s, want about 2s (3s budget − 1s reserve)", elapsed)
	}
	if !serverkit.Ended(ctx) {
		t.Fatal("the held stream must be ended with the drain cause")
	}
}

// DrainDeadline is the moment Drain actually hard-ends a claimed stream. With
// a budget under twice DrainReserve the reserve shrinks to half the budget, so
// "shutdown deadline − DrainReserve" — what a caller had to compute before —
// names a time that already passed.
func TestDrainDeadline_IsWhenDrainHardEndsTheStream(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams() // DrainReserve zero: DefaultDrainReserve (5s)
	ctx, release := streams.Bind(context.Background())
	if _, ok := serverkit.DrainDeadline(ctx); ok {
		t.Fatal("DrainDeadline must not hold before the drain begins")
	}
	soft := serverkit.Draining(ctx)
	type seen struct {
		deadline time.Time
		ok       bool
		endedAt  time.Time
	}
	got := make(chan seen, 1)
	go func() {
		defer release()
		<-soft
		d, ok := serverkit.DrainDeadline(ctx)
		<-ctx.Done()
		got <- seen{d, ok, time.Now()}
	}()

	const budget = 800 * time.Millisecond // < 2 × DefaultDrainReserve
	shutdown, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	end, _ := shutdown.Deadline()
	if err := streams.Drain(shutdown); err != nil {
		t.Fatalf("Drain = %v", err)
	}
	s := <-got
	if !s.ok {
		t.Fatal("DrainDeadline must hold for a stream woken by the soft phase")
	}
	// The reserve is half the budget left when Drain started: at most
	// budget/2, and only scheduling jitter less.
	if want := end.Add(-budget / 2); s.deadline.Before(want) || s.deadline.After(want.Add(50*time.Millisecond)) {
		t.Fatalf("DrainDeadline = shutdown deadline − %s, want − ~%s", end.Sub(s.deadline), budget/2)
	}
	if lag := s.endedAt.Sub(s.deadline); lag < 0 || lag > 100*time.Millisecond {
		t.Fatalf("the stream was ended %s after DrainDeadline, want at it", lag)
	}
}

// Through DrainHTTP the schedule is fixed before Shutdown's hook begins the
// soft phase, so a stream woken by it never reads "no deadline yet".
func TestDrainDeadline_HoldsUnderDrainHTTP(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	srv := serverkit.NewHTTPServer(serverkit.Config{Addr: "127.0.0.1:0"}, http.NotFoundHandler(), streams)
	ctx, release := streams.Bind(context.Background())
	soft := serverkit.Draining(ctx)
	ok := make(chan bool, 1)
	go func() {
		defer release()
		<-soft
		_, held := serverkit.DrainDeadline(ctx)
		ok <- held
	}()
	shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := serverkit.DrainHTTP(shutdown, srv, streams); err != nil {
		t.Fatalf("DrainHTTP = %v", err)
	}
	if !<-ok {
		t.Fatal("DrainDeadline must hold for every stream the soft phase wakes")
	}
}

// A budget already spent — a negative ShutdownTimeout is a window over as it
// opens — puts the hard deadline at the shutdown deadline, never after it.
func TestDrainDeadline_ASpentBudgetKeepsNoReserve(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	ctx, release := streams.Bind(context.Background())
	soft := serverkit.Draining(ctx)
	got := make(chan time.Time, 1)
	go func() {
		defer release()
		<-soft
		d, _ := serverkit.DrainDeadline(ctx)
		got <- d
	}()
	spent, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	end, _ := spent.Deadline()
	_ = streams.Drain(spent)
	if d := <-got; !d.Equal(end) {
		t.Fatalf("DrainDeadline = deadline %+v, want the deadline itself", d.Sub(end))
	}
}

// A drain with no deadline has no hard phase to report.
func TestDrainDeadline_FalseWithoutABudget(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	ctx, release := streams.Bind(context.Background())
	_ = serverkit.Draining(ctx)
	streams.BeginDrain()
	if _, ok := serverkit.DrainDeadline(ctx); ok {
		t.Fatal("a drain begun without Drain has no schedule")
	}
	release()
	if _, ok := serverkit.DrainDeadline(context.Background()); ok {
		t.Fatal("a context with no registry has no drain deadline")
	}
}

// An unclaimed stream must not wait for the reserve: it ends as the drain
// begins, so Drain returns as soon as its handler does.
func TestStreams_DrainDoesNotWaitOnUnclaimedStreams(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	ctx, release := streams.Bind(context.Background())
	go func() {
		<-ctx.Done()
		release()
	}()
	budget, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := streams.Drain(budget); err != nil {
		t.Fatalf("Drain = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Drain took %s for a stream that ended at once", elapsed)
	}
}

// A handler that ignores its context cannot hold Drain past the budget, and
// the leftover is reported.
func TestStreams_DrainReportsAStreamThatIgnoresItsContext(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	_, release := streams.Bind(context.Background())
	defer release()
	budget, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := streams.Drain(budget); err == nil {
		t.Fatal("Drain must report a stream still running at the deadline")
	}
}

func TestLongLived_BindsToTheInstalledRegistry(t *testing.T) {
	t.Parallel()
	streams := serverkit.NewStreams()
	ended := make(chan bool, 1)
	srv := httptest.NewServer(streams.Install(serverkit.LongLived(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		ended <- serverkit.Ended(r.Context())
	}))))
	defer srv.Close()

	resp, err := http.Get(srv.URL) //nolint:noctx // the server ends this request, not the client
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	deadline := time.Now().Add(2 * time.Second)
	for streams.Active() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	streams.BeginDrain()
	select {
	case drained := <-ended:
		if !drained {
			t.Fatal("the handler's request context must end with the drain cause")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the drain did not reach the LongLived handler's request context")
	}
}
