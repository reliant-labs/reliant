// Copyright (c) 2025 Reliant Labs
package file_system

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// windowsDaemonPaths are the shapes a Windows daemon reports. A POSIX API
// server must accept every one of them: only the daemon has a filesystem, and
// it may run on a different machine than this process, so path/filepath's
// host-OS rules are the wrong authority here.
var windowsDaemonPaths = []struct {
	name string
	path string
}{
	{"drive letter with backslashes", `C:\Users\sean\src\proj`},
	{"drive letter with forward slashes", "C:/Users/sean/src/proj"},
	{"UNC share", `\\server\share\proj`},
}

// TestFileSystemProxy_CreateDirectory_AcceptsWindowsDaemonPaths covers the
// project picker's "New folder" against a Windows daemon. The path is executed
// on the daemon, so it must reach it unmodified.
func TestFileSystemProxy_CreateDirectory_AcceptsWindowsDaemonPaths(t *testing.T) {
	for _, tc := range windowsDaemonPaths {
		t.Run(tc.name, func(t *testing.T) {
			router := &recordingFSDaemonRouter{}
			svc := newProxy(t, router, nil)
			ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")

			resp, err := svc.CreateDirectory(ctx, connect.NewRequest(&reliantv1.CreateDirectoryRequest{
				Path: tc.path,
			}))
			require.NoError(t, err)
			assert.Equal(t, tc.path, resp.Msg.Path)

			assert.Equal(t, "fs.mkdir", router.lastCommandType)
			var sent map[string]string
			require.NoError(t, json.Unmarshal(router.lastPayload, &sent))
			assert.Equal(t, tc.path, sent["path"], "the daemon must receive the path it gave us, byte for byte")
		})
	}
}
