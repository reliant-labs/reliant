// Copyright (c) 2025 Reliant Labs
package services

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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

// Note: DaemonStatus, NodeExecutionStatus, and ToolExecutionStatus
// are type aliases (=) to their proto enum types, so no conversion is needed.
// The db package types ARE the proto types.

// ============================================================================
// String → Proto Enum mappers for non-DB fields
// These are used where the source data is a string (e.g., background processes
// from in-memory shell manager, filesystem operations, package commands,
// settings, streaming).
// ============================================================================

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
