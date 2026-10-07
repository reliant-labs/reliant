// Copyright (c) 2025 Reliant Labs
package worktreesweep

import (
	"context"
	"strings"
)

// settingReader is the one thing KeepSetting needs from the settings store.
type settingReader interface {
	GetSettingValue(ctx context.Context, userID, key string) (string, bool)
}

// KeepSetting reads the user's "worktree.archive_cleanup_mode": "always_keep"
// means nothing is removed automatically.
type KeepSetting struct{ R settingReader }

// KeepFiles implements KeepFilesSetting.
func (k KeepSetting) KeepFiles(ctx context.Context, userID string) bool {
	v, ok := k.R.GetSettingValue(ctx, userID, "worktree.archive_cleanup_mode")
	return ok && strings.TrimSpace(v) == "always_keep"
}
