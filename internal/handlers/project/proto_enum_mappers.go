// Copyright (c) 2025 Reliant Labs
package project

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// fileChangeStatusFromString maps the daemon's git status word onto the wire
// enum.
func fileChangeStatusFromString(s string) reliantv1.FileChangeStatus {
	switch s {
	case "modified":
		return reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_MODIFIED
	case "staged":
		return reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_STAGED
	case "untracked":
		return reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_UNTRACKED
	case "deleted":
		return reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_DELETED
	default:
		return reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_UNSPECIFIED
	}
}
