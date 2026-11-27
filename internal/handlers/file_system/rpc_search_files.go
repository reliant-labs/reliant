package file_system

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// SearchFiles searches for text within files in the workspace.
func (s *Service) SearchFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.SearchFilesRequest],
) (*connect.Response[reliantv1.SearchFilesResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	searchPath := ""
	if req.Msg.Path != nil {
		searchPath = *req.Msg.Path
	}
	scope, err := s.resolveScope(ctx, userID, req.Msg.ProjectId, req.Msg.WorktreeId, req.Msg.ChatId)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := scope.resolve(searchPath)
	if err != nil {
		return nil, err
	}

	opts := map[string]any{
		"output_mode": "content",
		// Always set: resolvedPath is absolute and confined, and leaving
		// base_dir unset would let the daemon search from its own working
		// directory instead of the workspace.
		"base_dir": resolvedPath,
	}
	if req.Msg.FilePattern != nil {
		opts["file_glob"] = *req.Msg.FilePattern
	}
	if req.Msg.CaseSensitive != nil && !*req.Msg.CaseSensitive {
		opts["case_insensitive"] = true
	}
	if req.Msg.MaxResults != nil {
		opts["max_results"] = *req.Msg.MaxResults
	}
	if req.Msg.ContextLines != nil {
		opts["context_before"] = *req.Msg.ContextLines
		opts["context_after"] = *req.Msg.ContextLines
	}

	cmdReq := map[string]any{
		"pattern": req.Msg.Query,
		"opts":    opts,
	}

	var cmdResp struct {
		Matches   []daemonSearchMatch `json:"matches"`
		Truncated bool                `json:"truncated"`
	}
	if err := s.sendCommand(ctx, userID, scope.target, "fs.search", cmdReq, &cmdResp, fsScanTimeoutMs); err != nil {
		return nil, err
	}

	// Group matches by file path for the proto response format
	fileResults := make(map[string]*reliantv1.SearchResult)
	var fileOrder []string
	for _, m := range cmdResp.Matches {
		sr, exists := fileResults[m.File]
		if !exists {
			sr = &reliantv1.SearchResult{Path: m.File}
			fileResults[m.File] = sr
			fileOrder = append(fileOrder, m.File)
		}
		sr.Matches = append(sr.Matches, &reliantv1.SearchMatch{
			LineNumber:  int32(m.Line),
			LineContent: m.Content,
		})
	}

	results := make([]*reliantv1.SearchResult, 0, len(fileOrder))
	totalMatches := int32(0)
	for _, path := range fileOrder {
		sr := fileResults[path]
		totalMatches += int32(len(sr.Matches))
		results = append(results, sr)
	}

	return connect.NewResponse(&reliantv1.SearchFilesResponse{
		Results:      results,
		TotalMatches: totalMatches,
		Truncated:    cmdResp.Truncated,
	}), nil
}

// daemonSearchMatch mirrors daemon.SearchMatch for JSON deserialization.
type daemonSearchMatch struct {
	File       string `json:"file"`
	Line       int    `json:"line,omitempty"`
	Content    string `json:"content,omitempty"`
	MatchCount int    `json:"match_count,omitempty"`
}
