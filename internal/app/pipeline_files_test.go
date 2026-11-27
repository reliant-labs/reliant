// Copyright (c) 2025 Reliant Labs

package app

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/jobrun"
	"github.com/reliant-labs/reliant/internal/pipeline/practions"
)

type noPools struct{}

func (noPools) GetMachinePoolByName(context.Context, string, string) (*core.MachinePool, error) {
	return nil, core.ErrMachinePoolNotFound
}

func (noPools) GetMachinePoolForUser(context.Context, string, string) (*core.MachinePool, error) {
	return nil, core.ErrMachinePoolNotFound
}

func (noPools) ListMachinePoolMembers(context.Context, *core.MachinePool, string) ([]core.MachinePoolMemberRow, error) {
	return nil, nil
}

type noDaemons struct{}

func (noDaemons) SendDaemonCommandToDaemon(context.Context, string, string, string, []byte, int32) ([]byte, error) {
	return nil, errors.New("no daemon")
}

// A repository with no gate pool has no member to list its files: pr.next
// hears ErrFilesUnavailable, and judges the head high risk.
func TestPipelineFilesWithNoGatePoolIsUnavailable(t *testing.T) {
	t.Parallel()
	files, err := jobrun.NewFiles(jobrun.Deps{Pools: noPools{}, Daemons: noDaemons{}, PollInterval: 0})
	if err != nil {
		t.Fatal(err)
	}
	sha := "0123456789abcdef0123456789abcdef01234567"
	repo := &core.PipelineRepo{ID: "r1", ProjectID: "p1", Repository: "acme/app", Mode: core.PipelineRepoModeShadow, CreatedByUserID: "u1"}
	_, err = jobrunFiles{files: files}.ChangedFiles(t.Context(), repo, sha, sha)
	if !errors.Is(err, practions.ErrFilesUnavailable) || !errors.Is(err, jobrun.ErrNoMember) {
		t.Fatalf("no gate pool: %v", err)
	}
}

// No router, no Files: pr.next then reviews every head by model.
func TestPipelineFilesWithNoRouterIsNil(t *testing.T) {
	t.Parallel()
	f, err := pipelineFiles(nil, nil)
	if err != nil || f != nil {
		t.Fatalf("no router: %v, %v", f, err)
	}
}
