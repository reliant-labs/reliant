package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Timeouts (milliseconds) used for different operation categories.
const (
	timeoutFSStat    int32 = 5_000   // 5s for stat, list_dir
	timeoutFSDefault int32 = 30_000  // 30s for most FS ops
	timeoutFSSearch  int32 = 60_000  // 60s for search/glob/find_replace
	timeoutExecRun   int32 = 600_000 // 10min for synchronous command execution
	timeoutExecBG    int32 = 30_000  // 30s for bg start/kill/list
	timeoutExecOut   int32 = 30_000  // 30s for getting output
)

// CommandSender is the subset of DaemonRouter needed by RemoteClient.
// This avoids importing toolexec (which would risk circular deps).
type CommandSender interface {
	SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// RemoteClient proxies all Client operations to a remote daemon via
// CommandSender.SendDaemonCommand.
type RemoteClient struct {
	sender CommandSender
	userID string
}

// Compile-time check that RemoteClient implements Client.
var _ Client = (*RemoteClient)(nil)

// NewRemoteClient creates a RemoteClient that proxies operations to a
// remote daemon identified by userID.
func NewRemoteClient(sender CommandSender, userID string) *RemoteClient {
	return &RemoteClient{sender: sender, userID: userID}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// send marshals req to JSON, calls SendDaemonCommand, and unmarshals the
// response into resp. commandType is e.g. "fs.read_file".
func (r *RemoteClient) send(ctx context.Context, commandType string, req any, resp any, timeoutMs int32) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("remote %s: marshal request: %w", commandType, err)
	}
	raw, err := r.sender.SendDaemonCommand(ctx, r.userID, commandType, payload, timeoutMs)
	if err != nil {
		return fmt.Errorf("remote %s: %w", commandType, err)
	}
	if resp != nil {
		if err := json.Unmarshal(raw, resp); err != nil {
			return fmt.Errorf("remote %s: unmarshal response: %w", commandType, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// FileSystem implementation
// ---------------------------------------------------------------------------

type readFileRequest struct {
	Path string        `json:"path"`
	Opts *ReadFileOpts `json:"opts,omitempty"`
}

func (r *RemoteClient) ReadFile(ctx context.Context, path string, opts *ReadFileOpts) (*FileContent, error) {
	var resp FileContent
	if err := r.send(ctx, "fs.read_file", readFileRequest{Path: path, Opts: opts}, &resp, timeoutFSDefault); err != nil {
		return nil, err
	}
	return &resp, nil
}

type readBinaryFileRequest struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes"`
}

type readBinaryFileResponse struct {
	Data string `json:"data"` // base64-encoded
}

func (r *RemoteClient) ReadBinaryFile(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	var resp readBinaryFileResponse
	if err := r.send(ctx, "fs.read_binary_file", readBinaryFileRequest{Path: path, MaxBytes: maxBytes}, &resp, timeoutFSDefault); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("remote fs.read_binary_file: decode base64: %w", err)
	}
	return data, nil
}

type pdfPageCountRequest struct {
	Path string `json:"path"`
}

type pdfPageCountResponse struct {
	PageCount int `json:"page_count"`
}

func (r *RemoteClient) PDFPageCount(ctx context.Context, path string) (int, error) {
	var resp pdfPageCountResponse
	if err := r.send(ctx, "fs.pdf_page_count", pdfPageCountRequest{Path: path}, &resp, timeoutFSDefault); err != nil {
		return 0, err
	}
	return resp.PageCount, nil
}

type readPDFPagesRequest struct {
	Path  string `json:"path"`
	Pages string `json:"pages"`
}

type readPDFPagesResponse struct {
	Data string `json:"data"` // base64-encoded PDF bytes
}

func (r *RemoteClient) ReadPDFPages(ctx context.Context, path string, pages string) ([]byte, error) {
	var resp readPDFPagesResponse
	if err := r.send(ctx, "fs.read_pdf_pages", readPDFPagesRequest{Path: path, Pages: pages}, &resp, timeoutFSDefault); err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("remote fs.read_pdf_pages: decode base64: %w", err)
	}
	return data, nil
}

type writeFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (r *RemoteClient) WriteFile(ctx context.Context, path string, content string) (*WriteResult, error) {
	var resp WriteResult
	if err := r.send(ctx, "fs.write_file", writeFileRequest{Path: path, Content: content}, &resp, timeoutFSDefault); err != nil {
		return nil, err
	}
	return &resp, nil
}

type patchFileRequest struct {
	Path  string      `json:"path"`
	Edits []PatchEdit `json:"edits"`
}

func (r *RemoteClient) PatchFile(ctx context.Context, path string, edits []PatchEdit) (*PatchResult, error) {
	var resp PatchResult
	if err := r.send(ctx, "fs.patch_file", patchFileRequest{Path: path, Edits: edits}, &resp, timeoutFSDefault); err != nil {
		return nil, err
	}
	return &resp, nil
}

type statFileRequest struct {
	Path string `json:"path"`
}

func (r *RemoteClient) StatFile(ctx context.Context, path string) (*FileStat, error) {
	var resp FileStat
	if err := r.send(ctx, "fs.stat", statFileRequest{Path: path}, &resp, timeoutFSStat); err != nil {
		return nil, err
	}
	return &resp, nil
}

type listDirRequest struct {
	Path string `json:"path"`
}

func (r *RemoteClient) ListDirectory(ctx context.Context, path string) ([]DirEntry, error) {
	var resp struct {
		Path    string     `json:"path"`
		Entries []DirEntry `json:"entries"`
	}
	if err := r.send(ctx, "fs.list_dir", listDirRequest{Path: path}, &resp, timeoutFSStat); err != nil {
		return nil, err
	}
	return resp.Entries, nil
}

type globFilesRequest struct {
	Pattern string    `json:"pattern"`
	Opts    *GlobOpts `json:"opts,omitempty"`
}

func (r *RemoteClient) GlobFiles(ctx context.Context, pattern string, opts *GlobOpts) (*GlobResult, error) {
	var resp GlobResult
	if err := r.send(ctx, "fs.glob", globFilesRequest{Pattern: pattern, Opts: opts}, &resp, timeoutFSSearch); err != nil {
		return nil, err
	}
	return &resp, nil
}

type searchFilesRequest struct {
	Pattern string      `json:"pattern"`
	Opts    *SearchOpts `json:"opts,omitempty"`
}

func (r *RemoteClient) SearchFiles(ctx context.Context, pattern string, opts *SearchOpts) (*SearchResult, error) {
	var resp SearchResult
	if err := r.send(ctx, "fs.search", searchFilesRequest{Pattern: pattern, Opts: opts}, &resp, timeoutFSSearch); err != nil {
		return nil, err
	}
	return &resp, nil
}

type findReplaceRequest struct {
	Pattern     string           `json:"pattern"`
	Replacement string           `json:"replacement"`
	Opts        *FindReplaceOpts `json:"opts,omitempty"`
}

func (r *RemoteClient) FindReplace(ctx context.Context, pattern string, replacement string, opts *FindReplaceOpts) (*FindReplaceResult, error) {
	var resp FindReplaceResult
	if err := r.send(ctx, "fs.find_replace", findReplaceRequest{Pattern: pattern, Replacement: replacement, Opts: opts}, &resp, timeoutFSSearch); err != nil {
		return nil, err
	}
	return &resp, nil
}

type mkdirRequest struct {
	Path string `json:"path"`
}

func (r *RemoteClient) CreateDirectory(ctx context.Context, path string) error {
	return r.send(ctx, "fs.mkdir", mkdirRequest{Path: path}, nil, timeoutFSStat)
}

type deletePathRequest struct {
	Path string `json:"path"`
}

func (r *RemoteClient) DeletePath(ctx context.Context, path string) error {
	return r.send(ctx, "fs.delete", deletePathRequest{Path: path}, nil, timeoutFSStat)
}

// ---------------------------------------------------------------------------
// Executor implementation
// ---------------------------------------------------------------------------

func (r *RemoteClient) RunCommand(ctx context.Context, req *RunCommandRequest) (*CommandResult, error) {
	var resp CommandResult
	if err := r.send(ctx, "exec.run", req, &resp, timeoutExecRun); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (r *RemoteClient) StartBackground(ctx context.Context, req *RunCommandRequest) (string, error) {
	var resp struct {
		ProcessID string `json:"process_id"`
	}
	if err := r.send(ctx, "exec.bg_start", req, &resp, timeoutExecBG); err != nil {
		return "", err
	}
	return resp.ProcessID, nil
}

type getOutputRequest struct {
	ProcessID string      `json:"process_id"`
	Opts      *OutputOpts `json:"opts,omitempty"`
}

func (r *RemoteClient) GetProcessOutput(ctx context.Context, processID string, opts *OutputOpts) (*ProcessOutput, error) {
	var resp ProcessOutput
	if err := r.send(ctx, "exec.bg_output", getOutputRequest{ProcessID: processID, Opts: opts}, &resp, timeoutExecOut); err != nil {
		return nil, err
	}
	return &resp, nil
}

type killProcessRequest struct {
	ProcessID string `json:"process_id"`
}

func (r *RemoteClient) KillProcess(ctx context.Context, processID string) error {
	return r.send(ctx, "exec.bg_kill", killProcessRequest{ProcessID: processID}, nil, timeoutExecBG)
}

func (r *RemoteClient) ListProcesses(ctx context.Context) ([]*ProcessInfo, error) {
	var resp []*ProcessInfo
	if err := r.send(ctx, "exec.bg_list", struct{}{}, &resp, timeoutExecBG); err != nil {
		return nil, err
	}
	return resp, nil
}