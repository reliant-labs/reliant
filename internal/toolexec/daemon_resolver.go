// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"time"
)

// DaemonInfo describes a daemon instance.
type DaemonInfo struct {
	DaemonID   string
	Name       string
	Labels     map[string]string
	Type       string // "local" or "cloud"
	Status     string // "connected", "running", "suspended", "offline"
	LastActive time.Time
}

// DaemonSelector specifies criteria for selecting daemons.
type DaemonSelector struct {
	ID     string            // exact daemon ID
	Name   string            // match by name
	Type   string            // "local", "cloud", "any"
	Labels map[string]string // match by labels
}

// DaemonResolver resolves available daemons for a user given a selector.
// The default OSS implementation only resolves currently connected daemons.
// The control plane provides its own implementation that can query the DB.
type DaemonResolver interface {
	ResolveDaemons(ctx context.Context, userID string, selector *DaemonSelector) ([]DaemonInfo, error)
}

// DaemonWakeup can wake a suspended or offline daemon.
// The default OSS implementation is a no-op.
// The control plane provides its own that can resume suspended cloud daemons.
type DaemonWakeup interface {
	WakeupDaemon(ctx context.Context, daemonID string) error
}

// ConnectedDaemonLister is the subset of ToolsDaemonService needed by
// ConnectedDaemonResolver — avoids importing the services package.
type ConnectedDaemonLister interface {
	ListConnectedDaemons(userID string) []DaemonInfo
}

// ConnectedDaemonResolver is the default OSS resolver: it only returns
// daemons that are currently connected to this gateway instance.
type ConnectedDaemonResolver struct {
	lister ConnectedDaemonLister
}

// NewConnectedDaemonResolver creates a resolver backed by a connection lister.
func NewConnectedDaemonResolver(lister ConnectedDaemonLister) *ConnectedDaemonResolver {
	return &ConnectedDaemonResolver{lister: lister}
}

func (r *ConnectedDaemonResolver) ResolveDaemons(_ context.Context, userID string, selector *DaemonSelector) ([]DaemonInfo, error) {
	all := r.lister.ListConnectedDaemons(userID)
	if selector == nil {
		return all, nil
	}

	var filtered []DaemonInfo
	for _, d := range all {
		if selector.ID != "" && d.DaemonID != selector.ID {
			continue
		}
		if selector.Name != "" && d.Name != selector.Name {
			continue
		}
		if selector.Type != "" && selector.Type != "any" && d.Type != selector.Type {
			continue
		}
		if !labelsMatch(d.Labels, selector.Labels) {
			continue
		}
		filtered = append(filtered, d)
	}
	return filtered, nil
}

func labelsMatch(daemonLabels, selectorLabels map[string]string) bool {
	for k, v := range selectorLabels {
		if daemonLabels[k] != v {
			return false
		}
	}
	return true
}

// NoopDaemonWakeup is the default OSS wakeup that does nothing.
type NoopDaemonWakeup struct{}

func (NoopDaemonWakeup) WakeupDaemon(_ context.Context, _ string) error {
	return nil
}

// Compile-time interface checks.
var _ DaemonResolver = (*ConnectedDaemonResolver)(nil)
var _ DaemonWakeup = NoopDaemonWakeup{}
