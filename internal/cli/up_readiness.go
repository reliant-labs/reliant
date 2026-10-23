package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// waitHostReadiness keeps a missing listener distinct from an observed exit.
// The injected snapshot lets tests exercise delayed readiness and process
// failures without compiling a service or racing a real port scanner.
func waitHostReadiness(ctx context.Context, env string, e *KCLEntities, timeout, poll time.Duration, out io.Writer, snapshot func() []hostReadyResult) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	var nextProgress time.Time
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("[up] waiting for host services: %w", err)
		}
		unready := hostReadyUnready(snapshot())
		if len(unready) == 0 {
			return nil
		}
		for _, r := range unready {
			if r.state == portReadyForeign || r.exited != "" {
				return hostReadyError(env, unready, e)
			}
		}
		if time.Now().After(nextProgress) {
			ports := make([]string, 0, len(unready))
			for _, r := range unready {
				ports = append(ports, fmt.Sprintf("%s :%d", r.name, r.port))
			}
			fmt.Fprintf(out, "[up] waiting for host services (compilation + startup, timeout %s): %s; logs: %s/\n", timeout, strings.Join(ports, ", "), upLogDir(env))
			nextProgress = time.Now().Add(5 * time.Second)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("[up] waiting for host services: %w", ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("%w\n     readiness timed out after %s; processes may still be starting. For slower builds, use --host-ready-timeout (for example 5m)", hostReadyError(env, unready, e), timeout)
		case <-tick.C:
		}
	}
}

// exitReason only reports exits observed by Wait. An uninspectable process
// or an empty OS process snapshot is not proof that a runner died.
func (p *procRegistry) exitReason(name string) string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, mp := range p.processes {
		if mp.name != name || mp.done == nil {
			continue
		}
		select {
		case <-mp.done:
			if mp.waitErr != nil {
				return mp.waitErr.Error()
			}
			return "exit status 0"
		default:
		}
	}
	return ""
}

// observeExit reaps each child exactly once while Forge is alive. When Forge
// detaches and exits, running children keep their own process groups and log
// files; a Wait goroutine neither cancels nor kills them.
func (mp *managedProcess) observeExit(beforeWait func()) {
	mp.done = make(chan struct{})
	go func() {
		// StdoutPipe/StderrPipe must be drained before Wait closes them.
		if beforeWait != nil {
			beforeWait()
		}
		mp.waitErr = mp.cmd.Wait()
		close(mp.done)
	}()
}
