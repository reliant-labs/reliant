// Copyright (c) 2025 Reliant Labs
package file_system

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// interceptorDefault is interceptors.UnaryTimeout, the budget of an rpc the
// table does not name.
const interceptorDefault = 10 * time.Second

// fsRPCsWithinDefaultDeadline are the rpcs whose daemon budgets fit
// interceptorDefault, and so need no FileSystemRPCDeadlines entry.
var fsRPCsWithinDefaultDeadline = []string{
	reliantv1connect.FileSystemServiceListDirectoryProcedure,
	reliantv1connect.FileSystemServiceCreateDirectoryProcedure,
}

// TestFileSystemRPCDeadlinesCoverEveryProcedure pins the request deadline the
// api-server grants each FileSystemService method.
//
// The regression: no FileSystemService method had an entry, so every one
// inherited the interceptor's 10s default and was cancelled inside its own
// 30-60s daemon budget. GetFilePreview asks the machine for 60s to read up to
// 25 MB; over a slow home uplink it failed at 10s. Walking the service
// DESCRIPTOR rather than a hand list means a method added later fails here
// until it is given a budget (or listed as fitting the default).
func TestFileSystemRPCDeadlinesCoverEveryProcedure(t *testing.T) {
	deadlines := FileSystemRPCDeadlines()
	service := reliantv1.File_reliant_v1_filesystem_proto.Services().ByName("FileSystemService")
	require.NotNil(t, service, "FileSystemService descriptor not found")

	methods := service.Methods()
	for i := 0; i < methods.Len(); i++ {
		procedure := "/" + string(service.FullName()) + "/" + string(methods.Get(i).Name())
		deadline, ok := deadlines[procedure]
		if slices.Contains(fsRPCsWithinDefaultDeadline, procedure) {
			assert.False(t, ok, "%s is listed as fitting the default and has an entry too", procedure)
			continue
		}
		if !ok {
			t.Errorf("%s has no request deadline; it would inherit the %s interceptor default", procedure, interceptorDefault)
			continue
		}
		assert.Greater(t, deadline, interceptorDefault, procedure)
	}
}

// The request outlives every daemon command it sends, one after another, so
// the command's own budget fires first and its failure is classified.
func TestFileSystemRPCDeadlinesOutliveTheirDaemonCommands(t *testing.T) {
	ms := func(b int32) time.Duration { return time.Duration(b) * time.Millisecond }
	deadlines := FileSystemRPCDeadlines()

	for _, procedure := range []string{
		reliantv1connect.FileSystemServiceGetFilePreviewProcedure,
		reliantv1connect.FileSystemServiceDownloadFileProcedure,
	} {
		assert.Greater(t, deadlines[procedure], ms(homeDirTimeoutMs)+ms(previewInfoTimeoutMs)+ms(wholeFileReadTimeoutMs), procedure)
	}
	assert.Greater(t, deadlines[reliantv1connect.FileSystemServiceSearchFilesProcedure], ms(fsScanTimeoutMs))
	assert.Greater(t, deadlines[reliantv1connect.FileSystemServiceGetFileTreeProcedure], ms(fsCommandTimeoutMs))
	assert.Greater(t, deadlines[reliantv1connect.FileSystemServiceListMachineDirectoryProcedure], ms(machineListTimeoutMs))
	assert.LessOrEqual(t, ms(pickerTimeoutMs)+fsRPCHeadroom, interceptorDefault,
		"the picker's rpcs are listed as fitting the default")
}
