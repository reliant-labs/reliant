// Copyright (c) 2025 Reliant Labs

// Package prrecords keeps PR records: one record per pull request, and per
// head, mirrored from the provider's verified webhook deliveries
// (dev-docs/pr-records.md).
//
// A record holds facts — the pull request's state, head and labels, the
// pipeline markers its reviews carried, the check runs on its heads — never
// policy. Whose review counts, and what a label means, is the reader's to
// say (Policy, LabelPolicy), so the same record serves any pipeline.
//
// Records are fed only from the durable inbound path: the projector applies a
// delivery's normalized events after the receiver recorded it, and is
// idempotent per (provider, delivery key, event index), so a delivery retried
// after a crash applies nothing twice.
//
// Phase 1 is a shadow: records are written beside today's labels, read by the
// PullRequestRecordService and the `reliant pr-record diff` report, and
// nothing decides anything from them yet.
//
//forge:provided
package prrecords

import (
	"strings"
	"time"
)

// ProviderGitHub is the one provider records are kept for today.
const ProviderGitHub = "github"

// Delivery is one verified inbound delivery, as the receiver normalized it.
type Delivery struct {
	Provider string
	// Key is the provider's id for the delivery, stable across its
	// redeliveries (X-GitHub-Delivery).
	Key        string
	ReceivedAt time.Time
	Events     []Event
}

// Event is one normalized event of a delivery: the receiver's event type,
// routing keys, attributes and recorded data (trigger.payload.data).
type Event struct {
	Type        string
	AccountKey  string
	ResourceKey string
	Attributes  map[string]string
	Data        map[string]any
}

// Scope says which repositories records are kept for.
type Scope struct {
	repos map[string]bool
}

// NewScope keeps records for the listed owner/name repositories (matched
// case-insensitively). An empty list keeps none: records are opt-in.
func NewScope(repositories []string) Scope {
	s := Scope{repos: map[string]bool{}}
	for _, r := range repositories {
		if r = strings.ToLower(strings.TrimSpace(r)); r != "" {
			s.repos[r] = true
		}
	}
	return s
}

// Includes reports whether a repository's pull requests are recorded.
func (s Scope) Includes(provider, repository string) bool {
	return provider == ProviderGitHub && s.repos[strings.ToLower(repository)]
}

// Empty reports whether the scope records nothing.
func (s Scope) Empty() bool { return len(s.repos) == 0 }
