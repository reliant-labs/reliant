// Copyright (c) 2025 Reliant Labs

// Package projectproto is a project's wire form: as its owner reads it
// (Project), and reshaped for a viewer of a project its owner shared with
// their org (ForViewer; control-plane docs/design/org-sharing.md §4).
//
//forge:exclude-contract: stateless db-to-wire conversion of a project row; no state, nothing to mock
package projectproto

import (
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/chatproto"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Project converts a project row to its wire form, as its owner reads it.
func Project(p *core.Project) *reliantv1.Project {
	proto := &reliantv1.Project{
		Id:         p.ID,
		UserId:     p.UserID,
		Name:       p.Name,
		Path:       p.Path,
		IsGitRepo:  p.IsGitRepo,
		IsForge:    p.IsForge,
		CreatedAt:  p.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  p.UpdatedAt.Format(time.RFC3339),
		LastActive: p.LastActive.Format(time.RFC3339),
	}
	if p.Description != nil {
		proto.Description = p.Description
	}
	if p.DefaultBranch != nil {
		proto.DefaultBranch = p.DefaultBranch
	}
	if p.RemoteURL != nil {
		proto.RemoteUrl = p.RemoteURL
	}
	proto.OrgId = p.OrgID
	if p.ForgeProjectName != nil {
		proto.ForgeProjectName = p.ForgeProjectName
	}
	proto.Visibility = chatproto.Visibility(authz.ProjectVisibility(p))
	if p.SharedAt != nil {
		shared := p.SharedAt.UTC().Format(time.RFC3339)
		proto.SharedAt = &shared
	}
	return proto
}

// ForViewer reshapes out, a project as its owner reads it, for a viewer: it
// leaves out path (where the checkout lives on the owner's machine; a home
// directory names its owner) and sets viewer_access. remote_url stays: it is
// what the project is.
func ForViewer(out *reliantv1.Project) *reliantv1.Project {
	if out == nil {
		return nil
	}
	out.ViewerAccess = true
	out.Path = ""
	return out
}
