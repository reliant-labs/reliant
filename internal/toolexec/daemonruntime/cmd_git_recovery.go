// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

const (
	CommandGitRecoveryPublish = "git.recovery_publish"
	CommandGitRecoveryVerify  = "git.recovery_verify"
	CommandGitRecoveryDelete  = "git.recovery_delete"
)

// GitRecoveryRequest is an ephemeral daemon command, never a catalog record.
// Server callers authenticate the owner and resolve the registered repository
// identity and fresh credential before dispatch. Token must never be logged.
type GitRecoveryRequest struct {
	SourcePath string                          `json:"source_path,omitempty"`
	Capture    worktreereclaim.RecoveryCapture `json:"capture"`
	Token      string                          `json:"token"`
}
type GitRecoveryResponse struct {
	Receipt *worktreereclaim.RecoveryReceipt `json:"receipt,omitempty"`
	Held    bool                             `json:"held"`
	Detail  string                           `json:"detail,omitempty"`
	Deleted bool                             `json:"deleted,omitempty"`
}

func init() {
	RegisterCommand(CommandGitRecoveryPublish, handleGitRecoveryPublish)
	RegisterCommand(CommandGitRecoveryVerify, handleGitRecoveryVerify)
	RegisterCommand(CommandGitRecoveryDelete, handleGitRecoveryDelete)
}
func handleGitRecoveryPublish(ctx context.Context, payload []byte) ([]byte, error) {
	return handleGitRecovery(ctx, payload, CommandGitRecoveryPublish)
}
func handleGitRecoveryVerify(ctx context.Context, payload []byte) ([]byte, error) {
	return handleGitRecovery(ctx, payload, CommandGitRecoveryVerify)
}
func handleGitRecoveryDelete(ctx context.Context, payload []byte) ([]byte, error) {
	return handleGitRecovery(ctx, payload, CommandGitRecoveryDelete)
}
func handleGitRecovery(ctx context.Context, payload []byte, operation string) ([]byte, error) {
	var req GitRecoveryRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, errors.New("invalid git recovery payload")
	}
	token := req.Token
	req.Token = ""
	var receipt worktreereclaim.RecoveryReceipt
	var err error
	switch operation {
	case CommandGitRecoveryPublish:
		receipt, err = PublishGitRecovery(ctx, req.SourcePath, req.Capture, token)
	case CommandGitRecoveryVerify:
		receipt, err = VerifyGitRecovery(ctx, req.Capture, token)
	case CommandGitRecoveryDelete:
		err = DeleteGitRecovery(ctx, req.Capture, token)
	default:
		return nil, errors.New("unsupported git recovery operation")
	}
	if err != nil {
		// No raw output or callback errors across this boundary. Even a malicious
		// descriptor cannot cause a token or source contents to appear in detail.
		return json.Marshal(GitRecoveryResponse{Held: true, Detail: "Git recovery could not be completed safely; source and recovery catalog retained. Check repository identity, access, policy and connectivity."})
	}
	if operation == CommandGitRecoveryDelete {
		return json.Marshal(GitRecoveryResponse{Deleted: true})
	}
	return json.Marshal(GitRecoveryResponse{Receipt: &receipt})
}
