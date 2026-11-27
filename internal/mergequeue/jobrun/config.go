// Copyright (c) 2025 Reliant Labs
package jobrun

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/prrecords"
)

// ConfigReader reads a file under .reliant/ (the repository's
// pipeline.yaml) at a commit, as a config job on the repository's gate pool:
// the prrecords.Contents the PR record projector takes. It returns the bytes
// the member read, and the projector parses and validates them; nothing the
// member reports is trusted as a config.
//
// A read waits for a daemon, so its caller must not hold a webhook on it.
type ConfigReader struct{ d *dispatcher }

var _ prrecords.Contents = (*ConfigReader)(nil)

// NewConfigReader builds the ConfigReader. Deps.Pools, Deps.Daemons and
// Deps.Repos are required.
func NewConfigReader(deps Deps) (*ConfigReader, error) {
	if deps.Pools == nil || deps.Daemons == nil || deps.Repos == nil {
		return nil, errors.New("jobrun: NewConfigReader needs Pools, Daemons and Repos")
	}
	return &ConfigReader{d: newDispatcher(deps)}, nil
}

// Read implements prrecords.Contents. The file is read on the gate pool of
// the enrolment of req's repository by req's user. A file that is not at the
// commit is prrecords.ErrContentsNotFound.
func (c *ConfigReader) Read(ctx context.Context, req prrecords.ContentsRequest) ([]byte, error) {
	if !buildjob.ValidConfigPath(req.Path) {
		return nil, fmt.Errorf("%w: a config job reads only under %s, not %q", ErrInvalidRequest, buildjob.ConfigDir, req.Path)
	}
	repository := req.Owner + "/" + req.Repo
	repo, err := c.enrolment(ctx, repository, req.UserID, req.OrgID)
	if err != nil {
		return nil, err
	}
	t, err := c.d.target(ctx, repo)
	if err != nil {
		return nil, err
	}
	reply, member, err := c.d.run(ctx, t, buildjob.StartRequest{Kind: buildjob.KindConfig,
		Config: &buildjob.ConfigRequest{Repository: repo.Repository, SHA: req.Ref, Path: req.Path}}, "")
	if err != nil {
		return nil, err
	}
	res := reply.Config
	switch {
	case res == nil:
		return nil, fmt.Errorf("%w: machine %s reported no file", ErrUnverified, member)
	case !res.Found:
		return nil, prrecords.ErrContentsNotFound
	case len(res.Bytes) > buildjob.ConfigMaxBytes:
		return nil, fmt.Errorf("%w: machine %s reported %d bytes of %s, more than %d", ErrUnverified, member, len(res.Bytes), req.Path, buildjob.ConfigMaxBytes)
	}
	return res.Bytes, nil
}

// enrolment is the enrolment of repository that userID made (in orgID, when
// set).
func (c *ConfigReader) enrolment(ctx context.Context, repository, userID, orgID string) (*core.PipelineRepo, error) {
	if userID == "" {
		return nil, fmt.Errorf("%w: a config read names no user", ErrInvalidRequest)
	}
	rows, err := c.d.deps.Repos.ListPipelineReposByRepository(ctx, prrecords.ProviderGitHub, repository)
	if err != nil {
		return nil, fmt.Errorf("jobrun: find the enrolment of %s: %w", repository, err)
	}
	for _, r := range rows {
		if r != nil && r.CreatedByUserID == userID && (orgID == "" || r.OrgID == orgID) && strings.EqualFold(r.Repository, repository) {
			return r, nil
		}
	}
	return nil, fmt.Errorf("%w: %s has no enrolment by user %s", ErrInvalidRequest, repository, userID)
}
