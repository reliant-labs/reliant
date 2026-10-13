// Copyright (c) 2025 Reliant Labs

// Package processproto maps a background process's state, as the shell
// manager and the processes table spell it (strings), onto the proto enums
// the Background and PackageCommands services return.
//
//forge:exclude-contract: pure string-to-enum mapping; no I/O, state or time
package processproto

import reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

// --- BackgroundProcessStatus (from shell.BackgroundProcess in-memory, still string) ---

// Status maps a process status string onto the proto enum; an unknown one is
// UNSPECIFIED.
func Status(s string) reliantv1.BackgroundProcessStatus {
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

// StatusPtr is Status for an optional field: "" is no status.
func StatusPtr(s string) *reliantv1.BackgroundProcessStatus {
	if s == "" {
		return nil
	}
	v := Status(s)
	return &v
}

// --- OutputStreamType ---

// OutputStream maps "stdout"/"stderr" onto the proto enum.
func OutputStream(s string) reliantv1.OutputStreamType {
	switch s {
	case "stdout":
		return reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_STDOUT
	case "stderr":
		return reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_STDERR
	default:
		return reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_UNSPECIFIED
	}
}
