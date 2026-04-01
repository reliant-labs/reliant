// Copyright (c) 2025 Reliant Labs
package services

import (
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/pkgmgr"
)

// ============================================================================
// DB int32 → Proto Enum mappers
// DB stores int32 values that match proto enum numeric values.
// Conversion is a simple cast.
// ============================================================================

// --- PlanStatus ---

func planStatusFromInt32(v int32) reliantv1.PlanStatus {
	return reliantv1.PlanStatus(v)
}

// --- PlanComplexity ---

func planComplexityFromInt32(v int32) reliantv1.PlanComplexity {
	return reliantv1.PlanComplexity(v)
}

func planComplexityToInt32Ptr(e *reliantv1.PlanComplexity) *int32 {
	if e == nil {
		return nil
	}
	v := int32(*e)
	return &v
}

// --- TaskStatus ---

func taskStatusFromInt32(v int32) reliantv1.TaskStatus {
	return reliantv1.TaskStatus(v)
}

// Note: YieldStatus, DaemonStatus, NodeExecutionStatus, and ToolExecutionStatus
// are type aliases (=) to their proto enum types, so no conversion is needed.
// The db package types ARE the proto types.

// ============================================================================
// String → Proto Enum mappers for non-DB fields
// These are used where the source data is a string (e.g., background processes
// from in-memory shell manager, filesystem operations, package commands,
// settings, streaming).
// ============================================================================

// --- BackgroundProcessStatus (from shell.BackgroundProcess in-memory, still string) ---

func backgroundProcessStatusFromString(s string) reliantv1.BackgroundProcessStatus {
	switch s {
	case "running":
		return reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_RUNNING
	case "completed":
		return reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_COMPLETED
	case "failed":
		return reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_FAILED
	case "killed":
		return reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_KILLED
	case "killed_externally":
		return reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_KILLED_EXTERNALLY
	case "stale":
		return reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_STALE
	default:
		return reliantv1.BackgroundProcessStatus_BACKGROUND_PROCESS_STATUS_UNSPECIFIED
	}
}

func backgroundProcessStatusPtr(s string) *reliantv1.BackgroundProcessStatus {
	if s == "" {
		return nil
	}
	v := backgroundProcessStatusFromString(s)
	return &v
}

// --- OutputStreamType ---

func outputStreamTypeFromString(s string) reliantv1.OutputStreamType {
	switch s {
	case "stdout":
		return reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_STDOUT
	case "stderr":
		return reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_STDERR
	default:
		return reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_UNSPECIFIED
	}
}

// --- FileNodeType ---

func fileNodeTypeFromString(s string) reliantv1.FileNodeType {
	switch s {
	case "file":
		return reliantv1.FileNodeType_FILE_NODE_TYPE_FILE
	case "directory":
		return reliantv1.FileNodeType_FILE_NODE_TYPE_DIRECTORY
	default:
		return reliantv1.FileNodeType_FILE_NODE_TYPE_UNSPECIFIED
	}
}

// --- PackageType ---

func packageTypeFromString(s string) reliantv1.PackageType {
	switch s {
	case "npm":
		return reliantv1.PackageType_PACKAGE_TYPE_NPM
	case "makefile":
		return reliantv1.PackageType_PACKAGE_TYPE_MAKEFILE
	case "taskfile":
		return reliantv1.PackageType_PACKAGE_TYPE_TASKFILE
	default:
		return reliantv1.PackageType_PACKAGE_TYPE_UNSPECIFIED
	}
}

func pkgmgrPackageTypeFromProto(pt reliantv1.PackageType) (pkgmgr.PackageType, bool) {
	switch pt {
	case reliantv1.PackageType_PACKAGE_TYPE_NPM:
		return pkgmgr.PackageTypeNPM, true
	case reliantv1.PackageType_PACKAGE_TYPE_MAKEFILE:
		return pkgmgr.PackageTypeMakefile, true
	case reliantv1.PackageType_PACKAGE_TYPE_TASKFILE:
		return pkgmgr.PackageTypeTaskfile, true
	default:
		return "", false
	}
}

// --- FileChangeStatus ---

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

// --- ConfigSeverity ---

func configSeverityFromString(s string) reliantv1.ConfigSeverity {
	switch s {
	case "error":
		return reliantv1.ConfigSeverity_CONFIG_SEVERITY_ERROR
	case "warning":
		return reliantv1.ConfigSeverity_CONFIG_SEVERITY_WARNING
	default:
		return reliantv1.ConfigSeverity_CONFIG_SEVERITY_UNSPECIFIED
	}
}

// --- WorktreeStatus ---

func worktreeStatusFromInt32(v int32) reliantv1.WorktreeStatus {
	return reliantv1.WorktreeStatus(v)
}

// UserUpdateType and EntityType are now proto enum integers (stored as INTEGER in DB).
// No string conversion needed — the db layer scans integers and the streaming layer
// passes proto enums directly. See internal/db/models.go for type aliases.

// --- ChatUpdateType (from string for streaming_delta and other hardcoded uses) ---

func chatUpdateTypeFromString(s string) reliantv1.ChatUpdateType {
	normalized := strings.TrimSpace(strings.ToLower(s))
	normalized = strings.TrimPrefix(normalized, "chat_update_type_")

	switch normalized {
	case "message":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE
	case "approval":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_APPROVAL
	case "thread":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_THREAD
	case "tool_call":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL
	case "workflow_status":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_STATUS
	case "error":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_ERROR
	case "chat":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_CHAT
	case "run_output":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_RUN_OUTPUT
	case "node_execution":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_NODE_EXECUTION
	case "execution_log":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_EXECUTION_LOG
	case "workflow_execution":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WORKFLOW_EXECUTION
	case "yield":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_YIELD
	case "info":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_INFO
	case "warning":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_WARNING
	case "refetch":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_REFETCH
	case "streaming_delta":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAMING_DELTA
	case "skill_invocation":
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_SKILL_INVOCATION
	default:
		return reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_UNSPECIFIED
	}
}
