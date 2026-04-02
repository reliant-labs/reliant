// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/cmdutil"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/filepreview"
	"github.com/reliant-labs/reliant/internal/fileutil"
)

func init() {
	RegisterCommand("fs.read_file", handleFSReadFile)
	RegisterCommand("fs.write_file", handleFSWriteFile)
	RegisterCommand("fs.patch_file", handleFSPatchFile)
	RegisterCommand("fs.stat", handleFSStat)
	RegisterCommand("fs.list_dir", handleFSListDir)
	RegisterCommand("fs.glob", handleFSGlob)
	RegisterCommand("fs.search", handleFSSearch)
	RegisterCommand("fs.find_replace", handleFSFindReplace)
	RegisterCommand("fs.mkdir", handleFSMkdir)
	RegisterCommand("fs.delete", handleFSDelete)
	RegisterCommand("fs.get_tree", handleFSGetTree)
	RegisterCommand("fs.preview_info", handleFSPreviewInfo)
	RegisterCommand("fs.copy", handleFSCopy)
}

// =============================================================================
// fs.read_file
// =============================================================================

type fsReadFileRequest struct {
	Path string               `json:"path"`
	Opts *daemon.ReadFileOpts `json:"opts,omitempty"`
}

func handleFSReadFile(_ context.Context, payload []byte) ([]byte, error) {
	var req fsReadFileRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	f, err := os.Open(req.Path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", req.Path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", req.Path, err)
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 10*1024*1024) // up to 10MB lines

	var (
		lines      []string
		totalLines int
		offset     int
		limit      int
	)
	if req.Opts != nil {
		offset = req.Opts.Offset
		limit = req.Opts.Limit
	}

	lineNum := 0
	for scanner.Scan() {
		totalLines++
		if lineNum >= offset && (limit == 0 || len(lines) < limit) {
			lines = append(lines, scanner.Text())
		}
		lineNum++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", req.Path, err)
	}

	truncated := limit > 0 && totalLines > offset+limit

	resp := daemon.FileContent{
		Content:    strings.Join(lines, "\n"),
		TotalLines: totalLines,
		Truncated:  truncated,
		Size:       info.Size(),
	}
	return json.Marshal(resp)
}

// =============================================================================
// fs.write_file
// =============================================================================

type fsWriteFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func handleFSWriteFile(_ context.Context, payload []byte) ([]byte, error) {
	var req fsWriteFileRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	resp := daemon.WriteResult{}

	// Read old content if file exists
	oldBytes, err := os.ReadFile(req.Path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read old content %s: %w", req.Path, err)
		}
		resp.Created = true
	} else {
		resp.OldContent = string(oldBytes)
	}

	// Ensure parent directories exist
	if err := os.MkdirAll(filepath.Dir(req.Path), 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(req.Path), err)
	}

	content := []byte(req.Content)
	if err := os.WriteFile(req.Path, content, 0644); err != nil {
		return nil, fmt.Errorf("write %s: %w", req.Path, err)
	}

	info, err := os.Stat(req.Path)
	if err == nil {
		resp.ModTime = info.ModTime()
	}
	resp.BytesWritten = len(content)

	return json.Marshal(resp)
}

// =============================================================================
// fs.patch_file
// =============================================================================

type fsPatchFileRequest struct {
	Path  string             `json:"path"`
	Edits []daemon.PatchEdit `json:"edits"`
}

func handleFSPatchFile(_ context.Context, payload []byte) ([]byte, error) {
	var req fsPatchFileRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	data, err := os.ReadFile(req.Path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", req.Path, err)
	}

	content := string(data)
	resp := daemon.PatchResult{}

	for i, edit := range req.Edits {
		count := strings.Count(content, edit.OldString)
		if count == 0 {
			resp.Failed = append(resp.Failed, daemon.PatchFailed{
				Index:  i,
				Reason: "old_string not found",
			})
			continue
		}
		if edit.ReplaceAll {
			content = strings.ReplaceAll(content, edit.OldString, edit.NewString)
			resp.Applied = append(resp.Applied, daemon.PatchApplied{Index: i, Replaced: count})
		} else {
			if count > 1 {
				resp.Failed = append(resp.Failed, daemon.PatchFailed{
					Index:  i,
					Reason: fmt.Sprintf("old_string matches %d times (use replace_all for multiple)", count),
				})
				continue
			}
			content = strings.Replace(content, edit.OldString, edit.NewString, 1)
			resp.Applied = append(resp.Applied, daemon.PatchApplied{Index: i, Replaced: 1})
		}
	}

	if len(resp.Applied) > 0 {
		if err := os.WriteFile(req.Path, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("write %s: %w", req.Path, err)
		}
	}

	resp.NewContent = content
	return json.Marshal(resp)
}

// =============================================================================
// fs.stat
// =============================================================================

type fsStatRequest struct {
	Path string `json:"path"`
}

func handleFSStat(_ context.Context, payload []byte) ([]byte, error) {
	var req fsStatRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	info, err := os.Stat(req.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return json.Marshal(daemon.FileStat{Exists: false})
		}
		return nil, fmt.Errorf("stat %s: %w", req.Path, err)
	}

	resp := daemon.FileStat{
		Exists:  true,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
		Mode:    info.Mode().String(),
	}
	return json.Marshal(resp)
}

// =============================================================================
// fs.list_dir
// =============================================================================

type fsListDirRequest struct {
	Path string `json:"path"`
}

type fsListDirResponse struct {
	Path    string            `json:"path"`
	Entries []daemon.DirEntry `json:"entries"`
}

func handleFSListDir(_ context.Context, payload []byte) ([]byte, error) {
	var req fsListDirRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	path := req.Path
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		path = home
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", path, err)
	}

	result := make([]daemon.DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		var size int64
		if err == nil {
			size = info.Size()
		}
		result = append(result, daemon.DirEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  size,
		})
	}
	return json.Marshal(fsListDirResponse{
		Path:    path,
		Entries: result,
	})
}

// =============================================================================
// fs.glob
// =============================================================================

type fsGlobRequest struct {
	Pattern string           `json:"pattern"`
	Opts    *daemon.GlobOpts `json:"opts,omitempty"`
}

func handleFSGlob(_ context.Context, payload []byte) ([]byte, error) {
	var req fsGlobRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	baseDir := ""
	includeIgnored := false
	maxResults := 0
	if req.Opts != nil {
		if req.Opts.BaseDir != "" {
			baseDir = req.Opts.BaseDir
		}
		includeIgnored = req.Opts.IncludeIgnored
		maxResults = req.Opts.MaxResults
	}
	if baseDir == "" {
		var cwdErr error
		baseDir, cwdErr = os.Getwd()
		if cwdErr != nil {
			return nil, fmt.Errorf("failed to resolve working directory: %w", cwdErr)
		}
	}

	// Use ripgrep for fast glob (same approach as glob tool)
	rgCmd := fileutil.GetRgCmd(req.Pattern, includeIgnored)
	if rgCmd != nil {
		rgCmd.Dir = baseDir
		out, err := rgCmd.Output()
		if err == nil {
			var files []string
			for _, p := range bytes.Split(out, []byte{0}) {
				if len(p) == 0 {
					continue
				}
				relPath := string(p)
				if !includeIgnored && fileutil.ShouldSkip(relPath) {
					continue
				}
				absPath := relPath
				if !filepath.IsAbs(relPath) {
					absPath = filepath.Join(baseDir, relPath)
				}
				files = append(files, absPath)
				if maxResults > 0 && len(files) >= maxResults {
					break
				}
			}
			truncated := maxResults > 0 && len(files) >= maxResults
			return json.Marshal(daemon.GlobResult{Files: files, Truncated: truncated})
		}
		// Fall through to doublestar on rg failure
	}

	// Fallback: use doublestar
	limit := maxResults
	if limit == 0 {
		limit = 10000 // sensible default
	}
	files, truncated, err := fileutil.GlobWithDoublestar(req.Pattern, baseDir, limit, includeIgnored)
	if err != nil {
		return nil, fmt.Errorf("glob: %w", err)
	}
	return json.Marshal(daemon.GlobResult{Files: files, Truncated: truncated})
}

// =============================================================================
// fs.search
// =============================================================================

type fsSearchRequest struct {
	Pattern string             `json:"pattern"`
	Opts    *daemon.SearchOpts `json:"opts,omitempty"`
}

func handleFSSearch(_ context.Context, payload []byte) ([]byte, error) {
	var req fsSearchRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	baseDir := ""
	outputMode := "files_with_matches"
	var fileGlob, fileType string
	var contextBefore, contextAfter, maxResults int
	var caseInsensitive, fixedStrings, wordBoundary bool

	if req.Opts != nil {
		if req.Opts.BaseDir != "" {
			baseDir = req.Opts.BaseDir
		}
		if req.Opts.OutputMode != "" {
			outputMode = req.Opts.OutputMode
		}
		fileGlob = req.Opts.FileGlob
		fileType = req.Opts.FileType
		contextBefore = req.Opts.ContextBefore
		contextAfter = req.Opts.ContextAfter
		caseInsensitive = req.Opts.CaseInsensitive
		fixedStrings = req.Opts.FixedStrings
		wordBoundary = req.Opts.WordBoundary
		maxResults = req.Opts.MaxResults
	}
	if baseDir == "" {
		var cwdErr error
		baseDir, cwdErr = os.Getwd()
		if cwdErr != nil {
			return nil, fmt.Errorf("failed to resolve working directory: %w", cwdErr)
		}
	}

	rgPath, err := cmdutil.FindRipgrep()
	if err != nil {
		return nil, fmt.Errorf("ripgrep not found: %w", err)
	}

	args := []string{"--no-ignore", "--no-config", "--hidden"}

	switch outputMode {
	case "files_with_matches":
		args = append(args, "-l")
	case "count":
		args = append(args, "-c")
	case "content":
		args = append(args, "-H", "-n")
		if contextBefore > 0 {
			args = append(args, "-B", strconv.Itoa(contextBefore))
		}
		if contextAfter > 0 {
			args = append(args, "-A", strconv.Itoa(contextAfter))
		}
	}

	if caseInsensitive {
		args = append(args, "-i")
	}
	if wordBoundary {
		args = append(args, "-w")
	}
	if fixedStrings {
		args = append(args, "-F")
	}
	if fileType != "" {
		args = append(args, "--type", fileType)
	}
	if fileGlob != "" {
		args = append(args, "--glob", fileGlob)
	}

	// End of flags, then pattern
	args = append(args, "--", req.Pattern)

	// Set up search dir
	cmdDir := ""
	searchArg := baseDir
	if fileGlob != "" {
		cmdDir = baseDir
		searchArg = "."
	}
	args = append(args, searchArg)

	cmd := exec.Command(rgPath, args...)
	if cmdDir != "" {
		cmd.Dir = cmdDir
	}
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 1 || exitErr.ExitCode() == 2 {
				// No matches found or no files matched filter
				return json.Marshal(daemon.SearchResult{})
			}
		}
		return nil, fmt.Errorf("ripgrep failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var matches []daemon.SearchMatch

	for _, line := range lines {
		if line == "" || line == "--" {
			continue
		}

		switch outputMode {
		case "files_with_matches":
			fullPath := line
			if strings.HasPrefix(line, "./") {
				fullPath = filepath.Join(baseDir, strings.TrimPrefix(line, "./"))
			} else if !filepath.IsAbs(line) {
				fullPath = filepath.Join(baseDir, line)
			}
			matches = append(matches, daemon.SearchMatch{File: fullPath})

		case "count":
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				filePath := parts[0]
				if strings.HasPrefix(filePath, "./") {
					filePath = filepath.Join(baseDir, strings.TrimPrefix(filePath, "./"))
				} else if !filepath.IsAbs(filePath) {
					filePath = filepath.Join(baseDir, filePath)
				}
				cnt, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
				matches = append(matches, daemon.SearchMatch{File: filePath, MatchCount: cnt})
			}

		case "content":
			// Format: file:line:content
			parts := strings.SplitN(line, ":", 3)
			if len(parts) >= 3 {
				filePath := parts[0]
				if strings.HasPrefix(filePath, "./") {
					filePath = filepath.Join(baseDir, strings.TrimPrefix(filePath, "./"))
				} else if !filepath.IsAbs(filePath) {
					filePath = filepath.Join(baseDir, filePath)
				}
				lineNum, _ := strconv.Atoi(parts[1])
				matches = append(matches, daemon.SearchMatch{
					File:    filePath,
					Line:    lineNum,
					Content: parts[2],
				})
			}
		}

		if maxResults > 0 && len(matches) >= maxResults {
			break
		}
	}

	truncated := maxResults > 0 && len(matches) >= maxResults
	return json.Marshal(daemon.SearchResult{Matches: matches, Truncated: truncated})
}

// =============================================================================
// fs.find_replace
// =============================================================================

type fsFindReplaceRequest struct {
	Pattern     string                  `json:"pattern"`
	Replacement string                  `json:"replacement"`
	Opts        *daemon.FindReplaceOpts `json:"opts,omitempty"`
}

func handleFSFindReplace(_ context.Context, payload []byte) ([]byte, error) {
	var req fsFindReplaceRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	fileGlob := "**/*"
	var useRegex, ignoreCase, preview bool
	if req.Opts != nil {
		if req.Opts.FileGlob != "" {
			fileGlob = req.Opts.FileGlob
		}
		useRegex = req.Opts.UseRegex
		ignoreCase = req.Opts.IgnoreCase
		preview = req.Opts.Preview
	}

	// Compile the pattern
	var re *regexp.Regexp
	if useRegex {
		pattern := req.Pattern
		if ignoreCase {
			pattern = "(?i)" + pattern
		}
		var err error
		re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex pattern: %w", err)
		}
	}

	baseDir := ""
	if req.Opts != nil && req.Opts.BaseDir != "" {
		baseDir = req.Opts.BaseDir
	}
	if baseDir == "" {
		var cwdErr error
		baseDir, cwdErr = os.Getwd()
		if cwdErr != nil {
			return nil, fmt.Errorf("failed to resolve working directory: %w", cwdErr)
		}
	}

	// Find matching files using glob
	files, _, err := fileutil.GlobWithDoublestar(fileGlob, baseDir, 10000, false)
	if err != nil {
		return nil, fmt.Errorf("glob files: %w", err)
	}

	resp := daemon.FindReplaceResult{}

	for _, filePath := range files {
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		content := string(data)
		var newContent string
		var replacements int

		if useRegex && re != nil {
			matches := re.FindAllStringIndex(content, -1)
			replacements = len(matches)
			if replacements > 0 {
				newContent = re.ReplaceAllString(content, req.Replacement)
			}
		} else {
			findStr := req.Pattern
			searchIn := content
			if ignoreCase {
				searchIn = strings.ToLower(content)
				findStr = strings.ToLower(findStr)
			}
			replacements = strings.Count(searchIn, findStr)
			if replacements > 0 {
				if ignoreCase {
					// Case-insensitive literal replace
					re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(req.Pattern))
					if err == nil {
						newContent = re.ReplaceAllString(content, req.Replacement)
					}
				} else {
					newContent = strings.ReplaceAll(content, req.Pattern, req.Replacement)
				}
			}
		}

		if replacements == 0 {
			continue
		}

		change := daemon.FindReplaceChange{
			File:         filePath,
			Replacements: replacements,
		}

		if !preview {
			if err := os.WriteFile(filePath, []byte(newContent), 0644); err != nil {
				continue
			}
		}

		resp.FilesChanged++
		resp.Changes = append(resp.Changes, change)
	}

	return json.Marshal(resp)
}

// =============================================================================
// fs.mkdir
// =============================================================================

type fsMkdirRequest struct {
	Path string `json:"path"`
}

func handleFSMkdir(_ context.Context, payload []byte) ([]byte, error) {
	var req fsMkdirRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := os.MkdirAll(req.Path, 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", req.Path, err)
	}
	return json.Marshal(struct{}{})
}

// =============================================================================
// fs.delete
// =============================================================================

type fsDeleteRequest struct {
	Path string `json:"path"`
}

func handleFSDelete(_ context.Context, payload []byte) ([]byte, error) {
	var req fsDeleteRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := os.RemoveAll(req.Path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("delete %s: %w", req.Path, err)
	}
	return json.Marshal(struct{}{})
}

// =============================================================================
// fs.get_tree
// =============================================================================

type fsGetTreeRequest struct {
	Path       string `json:"path"`
	ShowHidden bool   `json:"show_hidden"`
}

type fsFileNode struct {
	Name     string        `json:"name"`
	Path     string        `json:"path"`
	Type     string        `json:"type"`
	Children []*fsFileNode `json:"children,omitempty"`
	Size     int64         `json:"size"`
	Modified string        `json:"modified,omitempty"`
}

// skipDirs contains directory names that are always skipped during tree walks.
var skipDirs = map[string]bool{
	"node_modules": true,
	"dist":         true,
	"__pycache__":  true,
	".git":         true,
}

func buildFileTree(fsPath string, relativePath string, showHidden bool) ([]*fsFileNode, error) {
	entries, err := os.ReadDir(fsPath)
	if err != nil {
		return nil, err
	}

	var nodes []*fsFileNode
	for _, entry := range entries {
		name := entry.Name()

		// Skip well-known noisy directories
		if entry.IsDir() && skipDirs[name] {
			continue
		}

		// Skip hidden files/dirs unless show_hidden is true
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}

		entryPath := filepath.Join(fsPath, name)
		relPath := filepath.Join(relativePath, name)

		info, err := entry.Info()
		if err != nil {
			continue
		}

		node := &fsFileNode{
			Name: name,
			Path: relPath,
		}

		if entry.IsDir() {
			node.Type = "directory"
			children, err := buildFileTree(entryPath, relPath, showHidden)
			if err == nil {
				node.Children = children
			}
		} else {
			node.Type = "file"
			node.Size = info.Size()
			node.Modified = info.ModTime().Format(time.RFC3339)
		}

		nodes = append(nodes, node)
	}

	return nodes, nil
}

func handleFSGetTree(_ context.Context, payload []byte) ([]byte, error) {
	var req fsGetTreeRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	basePath := req.Path
	if basePath == "" {
		var cwdErr error
		basePath, cwdErr = os.Getwd()
		if cwdErr != nil {
			return nil, fmt.Errorf("failed to resolve working directory: %w", cwdErr)
		}
	}

	nodes, err := buildFileTree(basePath, "", req.ShowHidden)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", basePath, err)
	}

	resp := struct {
		Nodes []*fsFileNode `json:"nodes"`
	}{Nodes: nodes}
	if resp.Nodes == nil {
		resp.Nodes = []*fsFileNode{}
	}
	return json.Marshal(resp)
}

// =============================================================================
// fs.preview_info
// =============================================================================

type fsPreviewInfoRequest struct {
	Path string `json:"path"`
}

type fsPreviewInfoResponse struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Modified   string `json:"modified"`
	ViewerKind string `json:"viewer_kind"`
	MIMEType   string `json:"mime_type"`
	IsBinary   bool   `json:"is_binary"`
	IsEditable bool   `json:"is_editable"`
}

func handleFSPreviewInfo(_ context.Context, payload []byte) ([]byte, error) {
	var req fsPreviewInfoRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	info, err := os.Stat(req.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", req.Path)
		}
		return nil, fmt.Errorf("stat %s: %w", req.Path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory: %s", req.Path)
	}

	// Read first 8KB for classification
	f, err := os.Open(req.Path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", req.Path, err)
	}
	defer f.Close()

	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read %s: %w", req.Path, err)
	}
	sample := buf[:n]

	classification := filepreview.Classify(req.Path, sample)

	resp := fsPreviewInfoResponse{
		Name:       info.Name(),
		Path:       req.Path,
		Size:       info.Size(),
		Modified:   info.ModTime().Format(time.RFC3339),
		ViewerKind: string(classification.ViewerKind),
		MIMEType:   classification.MIMEType,
		IsBinary:   classification.IsBinary,
		IsEditable: classification.IsEditable,
	}
	return json.Marshal(resp)
}

// =============================================================================
// fs.copy
// =============================================================================

type fsCopyRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

func handleFSCopy(_ context.Context, payload []byte) ([]byte, error) {
	var req fsCopyRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Source == "" || req.Destination == "" {
		return nil, fmt.Errorf("source and destination are required")
	}

	// Check source exists and is a file
	srcInfo, err := os.Stat(req.Source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("source not found: %s", req.Source)
		}
		return nil, fmt.Errorf("stat source %s: %w", req.Source, err)
	}
	if srcInfo.IsDir() {
		return nil, fmt.Errorf("source is a directory: %s", req.Source)
	}

	// Fail if destination already exists
	if _, err := os.Stat(req.Destination); err == nil {
		return nil, fmt.Errorf("destination already exists: %s", req.Destination)
	}

	// Read source
	content, err := os.ReadFile(req.Source)
	if err != nil {
		return nil, fmt.Errorf("read source %s: %w", req.Source, err)
	}

	// Ensure parent directories exist
	if err := os.MkdirAll(filepath.Dir(req.Destination), 0755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(req.Destination), err)
	}

	// Write destination
	if err := os.WriteFile(req.Destination, content, 0644); err != nil {
		return nil, fmt.Errorf("write destination %s: %w", req.Destination, err)
	}

	resp := struct {
		Message     string `json:"message"`
		Destination string `json:"destination"`
	}{
		Message:     "File copied successfully",
		Destination: req.Destination,
	}
	return json.Marshal(resp)
}
