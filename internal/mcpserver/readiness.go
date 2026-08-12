// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
)

// attachmentStaleThreshold is the freshness window for a daemon_attachment row
// to count as connected. It matches the daemon registry's window: two
// different answers to "is this daemon up" would show as a workspace the UI
// calls online and the connector calls unreachable.
const attachmentStaleThreshold = 90 * time.Second

// AttachmentReader is the slice of the repository this adapter needs.
type AttachmentReader interface {
	ListFreshDaemonAttachmentsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]*db.DaemonAttachment, error)
}

// DaemonResumer asks the platform to wake a suspended daemon.
//
// Waking is a control-plane capability: in OSS mode there is no orchestrator
// to schedule a pod, so this is optional and its absence is reported honestly
// rather than papered over with a wait that could never succeed.
type DaemonResumer interface {
	ResumeDaemon(ctx context.Context, userID, daemonID string) error
}

// AttachmentReadiness implements DaemonReadiness over daemon attachment rows.
type AttachmentReadiness struct {
	attachments AttachmentReader
	resumer     DaemonResumer
}

// NewAttachmentReadiness builds the adapter. resumer may be nil, in which case
// suspended daemons are reported as unavailable instead of being woken.
func NewAttachmentReadiness(attachments AttachmentReader, resumer DaemonResumer) *AttachmentReadiness {
	return &AttachmentReadiness{attachments: attachments, resumer: resumer}
}

// IsReady reports whether the daemon has a fresh attachment.
func (a *AttachmentReadiness) IsReady(ctx context.Context, userID, daemonID string) (bool, error) {
	if a.attachments == nil {
		return false, errors.New("no attachment source configured")
	}

	rows, err := a.attachments.ListFreshDaemonAttachmentsForUser(ctx, userID, attachmentStaleThreshold)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row != nil && row.DaemonID == daemonID {
			return true, nil
		}
	}
	return false, nil
}

// Resume asks the platform to wake the daemon.
func (a *AttachmentReadiness) Resume(ctx context.Context, userID, daemonID string) error {
	if a.resumer == nil {
		// Without an orchestrator there is nothing that could start this
		// workspace. Saying so beats waiting out a timeout that was never
		// going to resolve.
		return errors.New("this workspace is not running, and this deployment cannot start it automatically")
	}
	return a.resumer.ResumeDaemon(ctx, userID, daemonID)
}
