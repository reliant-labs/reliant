package file_system

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ReplaceInFiles replaces text in files across the workspace.
func (s *Service) ReplaceInFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.ReplaceInFilesRequest],
) (*connect.Response[reliantv1.ReplaceInFilesResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	// base_dir is the only thing bounding this walk. Omitting it (which an
	// empty base used to do) leaves the daemon to fall back to its own working
	// directory and rewrite files across the whole machine.
	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}

	opts := map[string]any{
		"base_dir": scope.basePath,
	}
	if req.Msg.FilePattern != nil {
		opts["file_glob"] = *req.Msg.FilePattern
	}
	if req.Msg.CaseSensitive != nil && !*req.Msg.CaseSensitive {
		opts["ignore_case"] = true
	}

	cmdReq := map[string]any{
		"pattern":     req.Msg.SearchText,
		"replacement": req.Msg.ReplaceText,
		"opts":        opts,
	}

	var cmdResp struct {
		FilesChanged int `json:"files_changed"`
		Changes      []struct {
			File         string `json:"file"`
			Replacements int    `json:"replacements"`
		} `json:"changes"`
	}
	if err := s.sendCommand(ctx, userID, scope.target, "fs.find_replace", cmdReq, &cmdResp, fsScanTimeoutMs); err != nil {
		return nil, err
	}

	results := make([]*reliantv1.ReplaceResult, len(cmdResp.Changes))
	totalReplacements := int32(0)
	for i, c := range cmdResp.Changes {
		results[i] = &reliantv1.ReplaceResult{
			Path:         c.File,
			Replacements: int32(c.Replacements),
			Success:      true,
		}
		totalReplacements += int32(c.Replacements)
	}

	return connect.NewResponse(&reliantv1.ReplaceInFilesResponse{
		Results:           results,
		TotalReplacements: totalReplacements,
		FilesModified:     int32(cmdResp.FilesChanged),
	}), nil
}
