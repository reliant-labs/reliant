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

// selectorDaemonResolver is implemented by routers that can resolve a selector
// to a daemon id without sending anything.
type selectorDaemonResolver interface {
	ResolveDaemonIDForSelector(ctx context.Context, userID string, selector *DaemonSelector) (string, error)
}

// ResolveDaemonIDForSelector returns the daemon a command sent with selector
// would reach: default resolution for nil, the id itself for an id selector.
//
// An operation that sends several commands AND records which daemon ran them
// (creating a worktree: one checkout per repo, then the owner on the row)
// resolves once here and pins every send with SendDaemonCommandToDaemon, so
// the commands and the record cannot name different machines.
func ResolveDaemonIDForSelector(ctx context.Context, router DaemonRouter, userID string, selector *DaemonSelector) (string, error) {
	if selector == nil {
		return router.ResolveDaemonID(ctx, userID)
	}
	if selector.ID != "" {
		return selector.ID, nil
	}
	if resolver, ok := router.(selectorDaemonResolver); ok {
		return resolver.ResolveDaemonIDForSelector(ctx, userID, selector)
	}
	return "", fmt.Errorf("daemon router cannot resolve a daemon selector")
}

// ResolveDaemonIDForSelector resolves a selector the way
// SendDaemonCommandWithSelector does, without sending.
func (r *NATSDaemonRouter) ResolveDaemonIDForSelector(ctx context.Context, userID string, selector *DaemonSelector) (string, error) {
	return r.resolveDaemonID(ctx, userID, selector)
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
