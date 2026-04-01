package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/cmdutil"
	"github.com/reliant-labs/reliant/internal/fileutil"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
)

// Compile-time check that LocalClient implements Client.
var _ Client = (*LocalClient)(nil)

// LocalClient implements Client by making direct OS and exec calls.
// It is used in monolith mode or when running on the daemon itself.
type LocalClient struct{}

// NewLocalClient creates a new LocalClient.
func NewLocalClient() *LocalClient {
	return &LocalClient{}
}

// ---------------------------------------------------------------------------
// FileSystem implementation
// ---------------------------------------------------------------------------

// ReadFile reads a file and returns its content, supporting offset/limit by
// line counting. It matches the behaviour of the view tool.
func (c *LocalClient) ReadFile(ctx context.Context, path string, opts *ReadFileOpts) (*FileContent, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	offset, limit := 0, 0
	if opts != nil {
		offset = opts.Offset
		limit = opts.Limit
	}

	scanner := bufio.NewScanner(f)
	// Allow up to 1 MB per line.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	// Skip lines before offset.
	currentLine := 0
	for currentLine < offset && scanner.Scan() {
		currentLine++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning %s: %w", path, err)
	}

	// Read requested lines.
	var lines []string
	for scanner.Scan() {
		if limit > 0 && len(lines) >= limit {
			break
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning %s: %w", path, err)
	}

	// Count remaining lines for totalLines.
	remaining := 0
	for scanner.Scan() {
		remaining++
	}
	totalLines := offset + len(lines) + remaining

	content := strings.Join(lines, "\n")
	truncated := (limit > 0 && len(lines) >= limit) || remaining > 0

	return &FileContent{
		Content:    content,
		TotalLines: totalLines,
		Truncated:  truncated,
		Size:       info.Size(),
	}, nil
}

// WriteFile writes content to a file, creating parent directories as needed.
func (c *LocalClient) WriteFile(ctx context.Context, path string, content string) (*WriteResult, error) {
	var oldContent string
	created := true

	existing, err := os.ReadFile(path)
	if err == nil {
		oldContent = string(existing)
		created = false
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading existing file %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating parent directories for %s: %w", path, err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("writing file %s: %w", path, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat after write %s: %w", path, err)
	}

	return &WriteResult{
		OldContent:   oldContent,
		Created:      created,
		ModTime:      info.ModTime(),
		BytesWritten: len(content),
	}, nil
}

// PatchFile applies a set of edits to a file.
func (c *LocalClient) PatchFile(ctx context.Context, path string, edits []PatchEdit) (*PatchResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading file %s: %w", path, err)
	}

	content := string(data)
	var applied []PatchApplied
	var failed []PatchFailed

	for i, edit := range edits {
		if edit.ReplaceAll {
			count := strings.Count(content, edit.OldString)
			if count == 0 {
				failed = append(failed, PatchFailed{Index: i, Reason: "old_string not found"})
				continue
			}
			content = strings.ReplaceAll(content, edit.OldString, edit.NewString)
			applied = append(applied, PatchApplied{Index: i, Replaced: count})
		} else {
			idx := strings.Index(content, edit.OldString)
			if idx == -1 {
				failed = append(failed, PatchFailed{Index: i, Reason: "old_string not found"})
				continue
			}
			lastIdx := strings.LastIndex(content, edit.OldString)
			if idx != lastIdx {
				failed = append(failed, PatchFailed{
					Index:  i,
					Reason: "old_string appears multiple times; use replace_all or provide more context",
				})
				continue
			}
			content = content[:idx] + edit.NewString + content[idx+len(edit.OldString):]
			applied = append(applied, PatchApplied{Index: i, Replaced: 1})
		}
	}

	if len(applied) > 0 {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return nil, fmt.Errorf("writing patched file %s: %w", path, err)
		}
	}

	return &PatchResult{
		NewContent: content,
		Applied:    applied,
		Failed:     failed,
	}, nil
}

// StatFile returns metadata about a file or directory.
func (c *LocalClient) StatFile(ctx context.Context, path string) (*FileStat, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &FileStat{Exists: false}, nil
		}
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}

	return &FileStat{
		Exists:  true,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
		Mode:    info.Mode().String(),
	}, nil
}

// ListDirectory lists entries in a directory (non-recursive).
func (c *LocalClient) ListDirectory(ctx context.Context, path string) ([]DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("reading directory %s: %w", path, err)
	}

	result := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		var size int64
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		result = append(result, DirEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  size,
		})
	}
	return result, nil
}

// GlobFiles finds files matching a glob pattern. Uses ripgrep when available,
// falling back to doublestar.
func (c *LocalClient) GlobFiles(ctx context.Context, pattern string, opts *GlobOpts) (*GlobResult, error) {
	baseDir := ""
	includeIgnored := false
	maxResults := 0

	if opts != nil {
		if opts.BaseDir != "" {
			baseDir = opts.BaseDir
		}
		includeIgnored = opts.IncludeIgnored
		maxResults = opts.MaxResults
	}
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve working directory: %w", err)
		}
	}

	// Normalize glob pattern.
	normResult := fileutil.NormalizeGlobPattern(pattern, baseDir)
	if normResult.ErrorMessage != "" {
		return nil, fmt.Errorf("invalid glob pattern: %s", normResult.ErrorMessage)
	}
	normalizedPattern := normResult.Pattern
	if normResult.PathAdjustment != "" {
		baseDir = filepath.Join(baseDir, normResult.PathAdjustment)
	}

	limit := maxResults
	if limit <= 0 {
		limit = 200 // default
	}

	// Try ripgrep first, fall back to doublestar.
	cmd := fileutil.GetRgCmd(normalizedPattern, includeIgnored)
	if cmd != nil {
		cmd.Dir = baseDir
		files, err := runRgGlob(cmd, baseDir, limit, includeIgnored)
		if err == nil {
			truncated := len(files) >= limit
			return &GlobResult{Files: files, Truncated: truncated}, nil
		}
		// Fall through to doublestar on error.
	}

	files, truncated, err := fileutil.GlobWithDoublestar(normalizedPattern, baseDir, limit, includeIgnored)
	if err != nil {
		return nil, fmt.Errorf("glob: %w", err)
	}

	return &GlobResult{Files: files, Truncated: truncated}, nil
}

// runRgGlob executes a ripgrep --files command and parses the output.
// Mirrors the logic in tools/glob.go:runRipgrep.
func runRgGlob(cmd *exec.Cmd, searchRoot string, limit int, includeIgnored bool) ([]string, error) {
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil // no matches
		}
		return nil, err
	}

	var matches []string
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
			absPath = filepath.Join(searchRoot, relPath)
		}
		matches = append(matches, absPath)
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return len(matches[i]) < len(matches[j])
	})

	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

// SearchFiles searches file contents using ripgrep.
func (c *LocalClient) SearchFiles(ctx context.Context, pattern string, opts *SearchOpts) (*SearchResult, error) {
	rgPath, err := cmdutil.FindRipgrep()
	if err != nil {
		return nil, fmt.Errorf("ripgrep not found: %w", err)
	}

	baseDir := ""
	outputMode := "files_with_matches"
	maxResults := 200
	if opts != nil {
		if opts.BaseDir != "" {
			baseDir = opts.BaseDir
		}
		if opts.OutputMode != "" {
			outputMode = opts.OutputMode
		}
		if opts.MaxResults > 0 {
			maxResults = opts.MaxResults
		}
	}
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve working directory: %w", err)
		}
	}

	args := buildRgSearchArgs(pattern, outputMode, opts)

	// When using glob, run from the search directory and use "." as path.
	searchArg := baseDir
	cmdDir := ""
	if opts != nil && opts.FileGlob != "" {
		cmdDir = baseDir
		searchArg = "."
	}
	args = append(args, searchArg)

	cmd := exec.CommandContext(ctx, rgPath, args...)
	if cmdDir != "" {
		cmd.Dir = cmdDir
	}
	output, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() == 1 {
				return &SearchResult{}, nil // no matches
			}
			if ee.ExitCode() == 2 {
				stderr := string(ee.Stderr)
				if stderr == "" || strings.Contains(stderr, "No files were searched") {
					return &SearchResult{}, nil
				}
				return nil, fmt.Errorf("ripgrep error: %s", strings.TrimSpace(stderr))
			}
		}
		return nil, fmt.Errorf("ripgrep failed: %w", err)
	}

	matches := parseRgOutput(string(output), outputMode, baseDir)

	// Filter ignored dirs unless IncludeIgnored is set.
	includeIgnored := opts != nil && opts.IncludeIgnored
	if !includeIgnored {
		filtered := make([]SearchMatch, 0, len(matches))
		for _, m := range matches {
			pathToCheck := m.File
			if filepath.IsAbs(m.File) {
				if rel, relErr := filepath.Rel(baseDir, m.File); relErr == nil {
					pathToCheck = rel
				}
			}
			pathToCheck = strings.TrimPrefix(pathToCheck, "./")
			if !fileutil.ShouldSkip(pathToCheck) {
				filtered = append(filtered, m)
			}
		}
		matches = filtered
	}

	truncated := len(matches) > maxResults
	if truncated {
		matches = matches[:maxResults]
	}

	return &SearchResult{Matches: matches, Truncated: truncated}, nil
}

// buildRgSearchArgs constructs ripgrep arguments from search options.
func buildRgSearchArgs(pattern, outputMode string, opts *SearchOpts) []string {
	args := []string{"--no-ignore", "--no-config", "--hidden"}

	switch outputMode {
	case "files_with_matches":
		args = append(args, "-l")
	case "count":
		args = append(args, "-c")
	case "content":
		args = append(args, "-H", "-n")
		if opts != nil {
			if opts.ContextBefore > 0 {
				args = append(args, "-B", strconv.Itoa(opts.ContextBefore))
			}
			if opts.ContextAfter > 0 {
				args = append(args, "-A", strconv.Itoa(opts.ContextAfter))
			}
		}
	}

	if opts != nil {
		if opts.CaseInsensitive {
			args = append(args, "-i")
		}
		if opts.WordBoundary {
			args = append(args, "-w")
		}
		if opts.FixedStrings {
			args = append(args, "-F")
		}
		if opts.Multiline {
			args = append(args, "-U", "--multiline-dotall")
		}
		if opts.FileType != "" {
			args = append(args, "--type", normalizeFileType(opts.FileType))
		}
		if opts.FileGlob != "" {
			args = append(args, "--glob", opts.FileGlob)
		}
	}

	// End of flags, then pattern.
	args = append(args, "--", pattern)
	return args
}

// normalizeFileType maps common type aliases to ripgrep type names.
func normalizeFileType(ft string) string {
	typeMap := map[string]string{
		"tsx":        "ts",
		"jsx":        "js",
		"typescript": "ts",
		"javascript": "js",
		"python":     "py",
		"golang":     "go",
		"csharp":     "cs",
		"cpp":        "c++",
	}
	if mapped, ok := typeMap[strings.ToLower(ft)]; ok {
		return mapped
	}
	return ft
}

// parseRgOutput parses ripgrep output into SearchMatch slices.
func parseRgOutput(output, outputMode, baseDir string) []SearchMatch {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var matches []SearchMatch

	for _, line := range lines {
		if line == "" || line == "--" {
			continue
		}

		switch outputMode {
		case "files_with_matches":
			filePath := resolveRgPath(line, baseDir)
			matches = append(matches, SearchMatch{File: filePath})

		case "count":
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				filePath := resolveRgPath(parts[0], baseDir)
				count, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
				matches = append(matches, SearchMatch{
					File:       filePath,
					MatchCount: count,
				})
			}

		case "content":
			parts := strings.SplitN(line, ":", 3)
			if len(parts) == 3 {
				filePath := resolveRgPath(parts[0], baseDir)
				lineNum, _ := strconv.Atoi(parts[1])
				matches = append(matches, SearchMatch{
					File:    filePath,
					Line:    lineNum,
					Content: parts[2],
				})
			}
		}
	}

	// Sort by mod time for files_with_matches mode.
	if outputMode == "files_with_matches" {
		type fileMod struct {
			match   SearchMatch
			modTime time.Time
		}
		fms := make([]fileMod, 0, len(matches))
		for _, m := range matches {
			var mt time.Time
			if info, err := os.Stat(m.File); err == nil {
				mt = info.ModTime()
			}
			fms = append(fms, fileMod{match: m, modTime: mt})
		}
		sort.Slice(fms, func(i, j int) bool {
			return fms[i].modTime.After(fms[j].modTime)
		})
		for i, fm := range fms {
			matches[i] = fm.match
		}
	}

	return matches
}

// resolveRgPath resolves a ripgrep output path to an absolute path.
func resolveRgPath(path, baseDir string) string {
	if filepath.IsAbs(path) {
		return path
	}
	path = strings.TrimPrefix(path, "./")
	return filepath.Join(baseDir, path)
}

// FindReplace performs find-and-replace across files matching a glob.
func (c *LocalClient) FindReplace(ctx context.Context, pattern string, replacement string, opts *FindReplaceOpts) (*FindReplaceResult, error) {
	baseDir := ""
	if opts != nil && opts.BaseDir != "" {
		baseDir = opts.BaseDir
	}
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve working directory: %w", err)
		}
	}
	var searchRegex *regexp.Regexp
	var err error

	if opts != nil && opts.UseRegex {
		flags := ""
		if opts.IgnoreCase {
			flags = "(?i)"
		}
		searchRegex, err = regexp.Compile(flags + pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex pattern: %w", err)
		}
	} else {
		escaped := regexp.QuoteMeta(pattern)
		flags := ""
		if opts != nil && opts.IgnoreCase {
			flags = "(?i)"
		}
		searchRegex, err = regexp.Compile(flags + escaped)
		if err != nil {
			return nil, fmt.Errorf("error compiling pattern: %w", err)
		}
	}

	// Find files to process.
	var filesToProcess []string
	if opts != nil && opts.FileGlob != "" {
		matches, _, err := fileutil.GlobWithDoublestar(opts.FileGlob, baseDir, 0, false)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern: %w", err)
		}
		filesToProcess = matches
	} else {
		// Use common text file patterns.
		patterns := []string{
			"**/*.go", "**/*.js", "**/*.ts", "**/*.tsx", "**/*.jsx",
			"**/*.py", "**/*.java", "**/*.c", "**/*.cpp", "**/*.h",
			"**/*.rs", "**/*.rb", "**/*.php", "**/*.swift",
			"**/*.json", "**/*.yaml", "**/*.yml", "**/*.toml",
			"**/*.xml", "**/*.html", "**/*.css", "**/*.scss",
			"**/*.md", "**/*.txt", "**/*.sql", "**/*.sh",
		}
		seen := make(map[string]bool)
		for _, p := range patterns {
			matches, _, err := fileutil.GlobWithDoublestar(p, baseDir, 0, false)
			if err != nil {
				continue
			}
			for _, m := range matches {
				if !seen[m] {
					seen[m] = true
					filesToProcess = append(filesToProcess, m)
				}
			}
		}
	}

	var changes []FindReplaceChange
	for _, filePath := range filesToProcess {
		info, err := os.Stat(filePath)
		if err != nil || info.IsDir() {
			continue
		}

		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		oldContent := string(data)
		matchIndices := searchRegex.FindAllStringIndex(oldContent, -1)
		if len(matchIndices) == 0 {
			continue
		}

		newContent := searchRegex.ReplaceAllString(oldContent, replacement)
		if oldContent == newContent {
			continue
		}

		preview := opts != nil && opts.Preview
		if !preview {
			if err := os.WriteFile(filePath, []byte(newContent), 0o644); err != nil {
				return nil, fmt.Errorf("writing file %s: %w", filePath, err)
			}
		}

		changes = append(changes, FindReplaceChange{
			File:         filePath,
			Replacements: len(matchIndices),
		})
	}

	return &FindReplaceResult{
		FilesChanged: len(changes),
		Changes:      changes,
	}, nil
}

// CreateDirectory creates a directory and all parent directories.
func (c *LocalClient) CreateDirectory(ctx context.Context, path string) error {
	return os.MkdirAll(path, 0o755)
}

// DeletePath removes a file or directory recursively.
func (c *LocalClient) DeletePath(ctx context.Context, path string) error {
	err := os.RemoveAll(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Executor implementation
// ---------------------------------------------------------------------------

// RunCommand executes a command synchronously using the system shell.
func (c *LocalClient) RunCommand(ctx context.Context, req *RunCommandRequest) (*CommandResult, error) {
	if req.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 60000 // 60s default
	}

	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	cmd := createShellCmd(cmdCtx, req.Command)
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}

	if len(req.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range req.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	exitCode := 0
	timedOut := false
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			timedOut = true
		}
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else if timedOut {
			exitCode = 124 // standard timeout exit code
		} else {
			exitCode = 1
		}
	}

	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()
	combined := stdout
	if stderr != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += stderr
	}

	return &CommandResult{
		ExitCode:   exitCode,
		Stdout:     stdout,
		Stderr:     stderr,
		Combined:   combined,
		DurationMs: duration.Milliseconds(),
		TimedOut:   timedOut,
	}, nil
}

// StartBackground starts a command in the background via the BackgroundManager.
func (c *LocalClient) StartBackground(ctx context.Context, req *RunCommandRequest) (string, error) {
	if req.Command == "" {
		return "", fmt.Errorf("command is required")
	}

	bgm := shell.GetBackgroundManager()
	process, err := bgm.StartProcess(ctx, shell.StartProcessOptions{
		Command:    req.Command,
		WorkingDir: req.WorkingDir,
		Env:        req.Env,
	})
	if err != nil {
		return "", fmt.Errorf("starting background process: %w", err)
	}
	return process.ID, nil
}

// GetProcessOutput retrieves output from a background process.
func (c *LocalClient) GetProcessOutput(ctx context.Context, processID string, opts *OutputOpts) (*ProcessOutput, error) {
	bgm := shell.GetBackgroundManager()

	stdout, stderr, err := bgm.GetOutput(processID)
	if err != nil {
		return nil, err
	}

	combined := stdout + stderr

	// Handle tail mode.
	if opts != nil && opts.TailLines > 0 {
		lines := strings.Split(combined, "\n")
		start := len(lines) - opts.TailLines
		if start < 0 {
			start = 0
		}
		tailContent := strings.Join(lines[start:], "\n")
		return &ProcessOutput{
			Output:     tailContent,
			HasMore:    start > 0,
			NextOffset: len(combined),
			TotalBytes: len(combined),
		}, nil
	}

	// Handle regex mode.
	if opts != nil && opts.Regex != "" {
		re, err := regexp.Compile(opts.Regex)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
		lines := strings.Split(combined, "\n")
		var filteredLines []string
		for _, line := range lines {
			if re.MatchString(line) {
				filteredLines = append(filteredLines, line)
			}
		}
		filtered := strings.Join(filteredLines, "\n")

		start := 0
		end := len(filtered)
		if opts.Offset > 0 {
			start = opts.Offset
		}
		if start > len(filtered) {
			start = len(filtered)
		}
		if opts.Limit > 0 && start+opts.Limit < end {
			end = start + opts.Limit
		}

		return &ProcessOutput{
			Output:     filtered[start:end],
			HasMore:    end < len(filtered),
			NextOffset: end,
			TotalBytes: len(filtered),
		}, nil
	}

	// Standard pagination.
	start := 0
	end := len(combined)
	if opts != nil {
		if opts.Offset > 0 {
			start = opts.Offset
		}
		if opts.Limit > 0 {
			end = start + opts.Limit
		}
	}
	if start > len(combined) {
		start = len(combined)
	}
	if end > len(combined) {
		end = len(combined)
	}

	return &ProcessOutput{
		Output:     combined[start:end],
		HasMore:    end < len(combined),
		NextOffset: end,
		TotalBytes: len(combined),
	}, nil
}

// KillProcess terminates a background process.
func (c *LocalClient) KillProcess(ctx context.Context, processID string) error {
	bgm := shell.GetBackgroundManager()
	err := bgm.KillProcess(processID)
	if err != nil && strings.Contains(err.Error(), "not running") {
		return nil // already exited, not an error per the interface contract
	}
	return err
}

// ListProcesses lists all background processes.
func (c *LocalClient) ListProcesses(ctx context.Context) ([]*ProcessInfo, error) {
	bgm := shell.GetBackgroundManager()
	all := bgm.GetAllProcesses()

	result := make([]*ProcessInfo, 0, len(all))
	for _, p := range all {
		result = append(result, &ProcessInfo{
			ID:        p.ID,
			Command:   p.Command,
			Status:    p.Status,
			ExitCode:  p.ExitCode,
			StartTime: p.StartTime,
			EndTime:   p.EndTime,
		})
	}
	return result, nil
}
