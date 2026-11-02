// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"slices"
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
	ID string // exact daemon ID
	// Name is what a caller calls the machine: its id, the name its owner gave
	// it, or its hostname. A workflow's `daemon:` shorthand lands here whether
	// the author wrote a name or a machine id, so matching tries the id first
	// (resolveNameAsID) and only then the name.
	Name   string
	Type   string            // "local", "cloud", "any"
	Labels map[string]string // match by labels
}

// resolveNameAsID returns the selector a Name means among the daemons isID
// recognises: when Name is exactly one of their ids (and no ID is set), the
// selector picks that daemon by id; otherwise it is returned unchanged and
// Name matches by name. An id outranks a name because it cannot be ambiguous:
// a machine named after another machine's id must not capture its requests.
//
// Without this, `daemon: e164d47d-…` in a workflow — a machine id, written
// where the shorthand was documented to accept one — matched no daemon by
// name, so every run's preflight failed and the queued delivery redelivered
// it every few seconds (prod 2026-10-29).
func resolveNameAsID(selector *DaemonSelector, isID func(string) bool) *DaemonSelector {
	if selector == nil || selector.ID != "" || selector.Name == "" || !isID(selector.Name) {
		return selector
	}
	byID := *selector
	byID.ID, byID.Name = selector.Name, ""
	return &byID
}

// DaemonResolver resolves available daemons for a user given a selector.
// The implementation here only resolves currently connected daemons; the
// NATS router falls back to the registry's records (lookupDaemonRecord).
type DaemonResolver interface {
	ResolveDaemons(ctx context.Context, userID string, selector *DaemonSelector) ([]DaemonInfo, error)
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
	selector = resolveNameAsID(selector, func(id string) bool {
		return slices.ContainsFunc(all, func(d DaemonInfo) bool { return d.DaemonID == id })
	})

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

// Compile-time interface checks.
var _ DaemonResolver = (*ConnectedDaemonResolver)(nil)
