// Copyright (c) 2025 Reliant Labs
package tools

// =============================================================================
// TOOL OUTPUT TYPES
// These define the structured output for tools that return typed data.
// Tools that return plain text should use string (nil OutputType).
// =============================================================================

// ViewOutput is the structured output from the view tool.
type ViewOutput struct {
	Content    string `json:"content"`
	LineCount  int    `json:"line_count"`
	Truncated  bool   `json:"truncated"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	TotalLines int    `json:"total_lines"`
}

// GrepOutput is the structured output from the grep tool.
type GrepOutput struct {
	Matches    []GrepMatch `json:"matches"`
	MatchCount int         `json:"match_count"`
	Truncated  bool        `json:"truncated"`
}

// GrepMatch represents a single grep match.
type GrepMatch struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

// GlobOutput is the structured output from the glob tool.
type GlobOutput struct {
	Files     []string `json:"files"`
	FileCount int      `json:"file_count"`
	Truncated bool     `json:"truncated"`
}

// BashOutput is the structured output from the bash tool.
type BashOutput struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// BashBackgroundOutput is returned when a command is started in the background.
type BashBackgroundOutput struct {
	ProcessID    string `json:"process_id"`
	Command      string `json:"command"`
	Backgrounded bool   `json:"backgrounded"`
}

// FetchOutput is the structured output from the fetch tool.
type FetchOutput struct {
	Content          string `json:"content"`
	ContentLength    int    `json:"content_length"`
	RawHTMLSize      int    `json:"raw_html_size,omitempty"`
	Truncated        bool   `json:"truncated"`
	EncodingUsed     string `json:"encoding_used"`
	PageTitle        string `json:"page_title,omitempty"`
	PossibleJSRender bool   `json:"possible_js_rendered,omitempty"`
	UsedReadability  bool   `json:"used_readability,omitempty"`
}

// WebSearchOutput is the structured output from the websearch tool.
type WebSearchOutput struct {
	Results []WebSearchResult `json:"results"`
	Count   int               `json:"count"`
}

// WebSearchResult represents a single search result.
type WebSearchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// EditOutput is the structured output from the edit tool.
type EditOutput struct {
	FilesChanged int      `json:"files_changed"`
	Files        []string `json:"files"`
}

// WriteOutput is the structured output from the write tool.
type WriteOutput struct {
	FilePath     string `json:"file_path"`
	BytesWritten int    `json:"bytes_written"`
	Created      bool   `json:"created"`
}
