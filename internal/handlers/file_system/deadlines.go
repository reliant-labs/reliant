// Copyright (c) 2025 Reliant Labs
package file_system

import (
	"time"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// The budgets this package gives each daemon command it sends, in
// milliseconds (the daemon envelope's unit). Every rpc dispatches with these
// names, and FileSystemRPCDeadlines derives the request deadlines from them,
// so a budget and the deadline that has to cover it cannot drift apart.
const (
	// homeDirTimeoutMs bounds skills.get_home_dir, which a viewer read asks
	// only for a "~/..." path (resolveForView).
	homeDirTimeoutMs int32 = 10_000
	// previewInfoTimeoutMs bounds fs.preview_info: a stat plus a look at the
	// file's first bytes.
	previewInfoTimeoutMs int32 = 30_000
	// fsCommandTimeoutMs bounds one ordinary file command: a read, write,
	// stat, mkdir, copy or delete, or the tree walk.
	fsCommandTimeoutMs int32 = 30_000
	// fsScanTimeoutMs bounds a command that visits the whole workspace:
	// content search and replace.
	fsScanTimeoutMs int32 = 60_000
	// wholeFileReadTimeoutMs bounds fs.read_binary_file of a file up to the
	// 25 MB whole-file cap (GetFilePreview, DownloadFile): a few chunked NATS
	// round trips, on a home uplink as much as in a datacenter.
	wholeFileReadTimeoutMs int32 = 60_000
	// pickerTimeoutMs bounds the project picker's list and mkdir, which run
	// while a person waits on a dialog.
	pickerTimeoutMs int32 = 5_000
	// machineListTimeoutMs bounds fs.list_machine_dir: one directory read
	// plus a stat of each entry (up to 5000), anywhere on the machine,
	// network mounts included.
	machineListTimeoutMs int32 = 15_000
)

// fsRPCHeadroom is how much longer a request may live than the daemon
// commands it sends. The command's own budget must fire first: it is what
// turns a slow machine into a classified answer, whereas the request deadline
// firing first cancels the context and the user sees a bare DeadlineExceeded.
const fsRPCHeadroom = 5 * time.Second

// FileSystemRPCDeadlines is the request deadline each FileSystemService rpc
// needs, for the api-server's Deadline slot (interceptors.ProcedureTimeouts):
// the sum of the budgets of the daemon commands it can send one after the
// other, plus fsRPCHeadroom.
//
// Without it every budget above 10s was dead code: no FileSystemService rpc
// had an entry, so each inherited the interceptor's 10s default, and the
// router shrank the daemon's budget to match (callerBoundedTimeoutMs). A
// 25 MB preview over a slow uplink failed at 10s whatever its 60s budget said.
//
// The project picker's rpcs (ListDirectory, CreateDirectory) fit the default
// and have no entry. TestFileSystemRPCDeadlinesCoverEveryProcedure walks the
// service descriptor, so an rpc added later fails until it is placed in one
// group or the other.
func FileSystemRPCDeadlines() map[string]time.Duration {
	seq := func(budgets ...int32) time.Duration {
		total := fsRPCHeadroom
		for _, b := range budgets {
			total += time.Duration(b) * time.Millisecond
		}
		return total
	}
	return map[string]time.Duration{
		reliantv1connect.FileSystemServiceGetFileTreeProcedure:          seq(fsCommandTimeoutMs),
		reliantv1connect.FileSystemServiceGetFileContentProcedure:       seq(homeDirTimeoutMs, fsCommandTimeoutMs),
		reliantv1connect.FileSystemServiceSaveFileContentProcedure:      seq(fsCommandTimeoutMs),
		reliantv1connect.FileSystemServiceGetFileMetadataProcedure:      seq(homeDirTimeoutMs, fsCommandTimeoutMs),
		reliantv1connect.FileSystemServiceGetFilePreviewInfoProcedure:   seq(homeDirTimeoutMs, previewInfoTimeoutMs),
		reliantv1connect.FileSystemServiceGetFilePreviewProcedure:       seq(homeDirTimeoutMs, previewInfoTimeoutMs, wholeFileReadTimeoutMs),
		reliantv1connect.FileSystemServiceCreateFileOrFolderProcedure:   seq(fsCommandTimeoutMs),
		reliantv1connect.FileSystemServiceDeleteFileOrFolderProcedure:   seq(fsCommandTimeoutMs),
		reliantv1connect.FileSystemServiceCopyFileProcedure:             seq(fsCommandTimeoutMs),
		reliantv1connect.FileSystemServiceSearchFilesProcedure:          seq(fsScanTimeoutMs),
		reliantv1connect.FileSystemServiceReplaceInFilesProcedure:       seq(fsScanTimeoutMs),
		reliantv1connect.FileSystemServiceDownloadFileProcedure:         seq(homeDirTimeoutMs, previewInfoTimeoutMs, wholeFileReadTimeoutMs),
		reliantv1connect.FileSystemServiceListMachineDirectoryProcedure: seq(machineListTimeoutMs),
	}
}
