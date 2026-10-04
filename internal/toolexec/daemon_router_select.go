package toolexec

import (
	"context"
	"fmt"
)

type daemonSelectorContextKey struct{}

// WithDaemonSelector records the daemon a run's tools must execute on, so
// tool-time code that only receives a context (the MCP runtime) lands on the
// same daemon as built-in tools. A nil selector leaves default resolution.
func WithDaemonSelector(ctx context.Context, selector *DaemonSelector) context.Context {
	if selector == nil {
		return ctx
	}
	return context.WithValue(ctx, daemonSelectorContextKey{}, selector)
}

// DaemonSelectorFromContext returns the selector recorded by WithDaemonSelector.
func DaemonSelectorFromContext(ctx context.Context) *DaemonSelector {
	selector, _ := ctx.Value(daemonSelectorContextKey{}).(*DaemonSelector)
	return selector
}

// selectorCommandSender is implemented by routers that can resolve a selector
// to a daemon for a generic command.
type selectorCommandSender interface {
	SendDaemonCommandWithSelector(ctx context.Context, userID string, selector *DaemonSelector, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// SendDaemonCommandForSelector sends a generic command the way built-in daemon
// tools are routed: nil selector means default resolution, otherwise the
// selected daemon.
func SendDaemonCommandForSelector(ctx context.Context, router DaemonRouter, userID string, selector *DaemonSelector, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if selector == nil {
		return router.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	}
	if sender, ok := router.(selectorCommandSender); ok {
		return sender.SendDaemonCommandWithSelector(ctx, userID, selector, commandType, payload, timeoutMs)
	}
	if selector.ID != "" {
		return router.SendDaemonCommandToDaemon(ctx, userID, selector.ID, commandType, payload, timeoutMs)
	}
	return nil, fmt.Errorf("daemon router cannot resolve a daemon selector for %s", commandType)
}

// SendDaemonCommandWithSelector resolves the selector to a daemon id and sends
// the command to it.
func (r *NATSDaemonRouter) SendDaemonCommandWithSelector(ctx context.Context, userID string, selector *DaemonSelector, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	data, reqID, err := r.buildDaemonCommand(ctx, commandType, payload, timeoutMs)
	if err != nil {
		return nil, err
	}
	daemonID, err := r.resolveDaemonID(ctx, userID, selector)
	if err != nil {
		return nil, fmt.Errorf("resolving daemon for command: %w", err)
	}
	return r.sendDaemonCommandData(ctx, userID, daemonID, commandType, data, reqID, timeoutMs)
}
