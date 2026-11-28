package file_system

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListDirectory lists entries in an arbitrary filesystem directory.
func (s *Service) ListDirectory(
	ctx context.Context,
	req *connect.Request[reliantv1.ListDirectoryRequest],
) (*connect.Response[reliantv1.ListDirectoryResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	path := req.Msg.GetPath()

	cmdReq := map[string]any{
		"path": path,
	}

	var cmdResp struct {
		// When path is empty the daemon resolves to home and returns it.
		Path    string            `json:"path"`
		Entries []fsProxyDirEntry `json:"entries"`
	}
	// No workspace to follow: the project picker browses the machine it
	// names, else the default machine.
	target, err := s.pickerMachine(ctx, userID, req.Msg.GetDaemonId())
	if err != nil {
		return nil, err
	}
	if err := s.sendCommand(ctx, userID, target, "fs.list_dir", cmdReq, &cmdResp, pickerTimeoutMs); err != nil {
		return nil, err
	}

	resolvedPath := cmdResp.Path
	if resolvedPath == "" {
		resolvedPath = path
	}

	entries := make([]*reliantv1.DirectoryEntry, 0, len(cmdResp.Entries))
	for _, e := range cmdResp.Entries {
		fullPath := e.Name
		if resolvedPath != "" {
			fullPath = resolvedPath + "/" + e.Name
		}
		entries = append(entries, &reliantv1.DirectoryEntry{
			Name:        e.Name,
			Path:        fullPath,
			IsDirectory: e.IsDir,
			IsHidden:    len(e.Name) > 0 && e.Name[0] == '.',
			IsSymlink:   e.IsSymlink,
		})
	}

	return connect.NewResponse(&reliantv1.ListDirectoryResponse{
		Path:     resolvedPath,
		Entries:  entries,
		DaemonId: pickerEcho(target),
	}), nil
}

// fsProxyDirEntry mirrors the daemon's DirEntry JSON shape.
type fsProxyDirEntry struct {
	Name      string `json:"name"`
	IsDir     bool   `json:"is_dir"`
	Size      int64  `json:"size"`
	IsSymlink bool   `json:"is_symlink"`
}
