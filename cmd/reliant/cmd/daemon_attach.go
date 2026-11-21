// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
)

// daemonAttachPoll is how often `daemon start --attach` checks whether the
// stream is up. A variable so tests need not wait.
var daemonAttachPoll = time.Second

// projectCreator is the slice of the ProjectService client --attach needs.
type projectCreator interface {
	CreateProject(context.Context, *connect.Request[reliantv1.CreateProjectRequest]) (*connect.Response[reliantv1.CreateProjectResponse], error)
}

// attachCheckoutWhenConnected waits until the daemon in dataDir has an
// established stream and an assigned id, then links dir as a project on this
// machine: CreateProject with daemon_id attaches the project whose remote dir
// is a checkout of, or creates a local one; AlreadyExists means it is already
// linked here. It prints one line to w. Every failure is a warning: the
// daemon keeps running, so nothing here returns an error.
func attachCheckoutWhenConnected(ctx context.Context, w io.Writer, dataDir, dir string, client func() (projectCreator, error), poll time.Duration) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(w, "Warning: --attach %s: %v\n", dir, err)
		return
	}
	var daemonID string
	for {
		if daemonConnected(dataDir) {
			if daemonID = bootstrap.ReadDaemonID(dataDir); daemonID != "" {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
	c, err := client()
	if err != nil {
		fmt.Fprintf(w, "Warning: could not link %s to Reliant: %v\n", absDir, err)
		return
	}
	resp, err := c.CreateProject(ctx, connect.NewRequest(&reliantv1.CreateProjectRequest{
		Name:     filepath.Base(absDir),
		Path:     absDir,
		DaemonId: &daemonID,
	}))
	switch {
	case connect.CodeOf(err) == connect.CodeAlreadyExists:
		fmt.Fprintf(w, "%s is already a project on this machine\n", absDir)
	case err != nil:
		fmt.Fprintf(w, "Warning: could not link %s to Reliant (the daemon keeps running): %v\n", absDir, err)
	default:
		p := resp.Msg.GetProject()
		fmt.Fprintf(w, "Linked %s to project %q (%s) on this machine\n", absDir, p.GetName(), p.GetId())
	}
}
