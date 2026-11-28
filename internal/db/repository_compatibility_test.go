// Copyright (c) 2025 Reliant Labs
package db

import "context"

// Keep the deprecated callable surface while callers migrate to store extensions.
var _ func(*Repo, context.Context, string) (string, error) = (*Repo).GetLiveChatIDForWorktree
var _ func(*Repo, context.Context, []string) (map[string]bool, error) = (*Repo).ListWorktreeIDsWithRunningChat
var _ func(*Repo, context.Context, string, string, bool) (bool, error) = (*Repo).ResolveThreadAmbientMemoryExclusion
