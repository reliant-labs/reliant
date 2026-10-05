// Copyright (c) 2025 Reliant Labs
package local

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
)

type fakeRepoSource struct {
	stored    map[string]string
	hostnames map[string]string
	attached  []string
}

func newFakeRepoSource(stored, hostnames map[string]string, attached []string) fakeRepoSource {
	return fakeRepoSource{stored: stored, hostnames: hostnames, attached: attached}
}

func (f fakeRepoSource) ListDaemonLocalModels(context.Context, string) (map[string]string, error) {
	return f.stored, nil
}

func (f fakeRepoSource) ListDaemonsByUserID(context.Context, string) ([]*db.Daemon, error) {
	var out []*db.Daemon
	for id, host := range f.hostnames {
		host := host
		out = append(out, &db.Daemon{ID: id, Hostname: &host})
	}
	return out, nil
}

func (f fakeRepoSource) ListFreshDaemonAttachmentsForUser(context.Context, string, time.Duration) ([]*db.DaemonAttachment, error) {
	var out []*db.DaemonAttachment
	for _, id := range f.attached {
		out = append(out, &db.DaemonAttachment{DaemonID: id})
	}
	return out, nil
}
