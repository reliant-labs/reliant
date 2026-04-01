// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/PuerkitoBio/goquery"
	readability "github.com/go-shiori/go-readability"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type FetchParams struct {
	URL     string `json:"url" jsonschema:"required,description=The URL to fetch content from"`
	Format  string `json:"format" jsonschema:"required,enum=text,enum=markdown,enum=html,description=The format to return the content in (text markdown or html)"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"description=Optional timeout in seconds (max 120)"`
	MaxSize int    `json:"max_size,omitempty" jsonschema:"description=Maximum bytes to fetch (default: 16000)"`
}

type FetchPermissionsParams struct {
	URL     string `json:"url"`
	Format  string `json:"format"`
	Timeout int    `json:"timeout,omitempty"`
	MaxSize int    `json:"max_size,omitempty"`
}

type FetchResponseMetadata struct {
	ContentLength    int    `json:"content_length"`
	RawHTMLSize      int    `json:"raw_html_size,omitempty"`
	Truncated        bool   `json:"truncated"`
	EncodingUsed     string `json:"encoding_used"`
	StatusCode       int    `json:"status_code"`
	PageTitle        string `json:"page_title,omitempty"`
	PossibleJSRender bool   `json:"possible_js_rendered,omitempty"`
	UsedReadability  bool   `json:"used_readability,omitempty"`
}

type fetchTool struct {
	client *http.Client
}

const (
	FetchToolName        = "fetch"
	fetchToolDescription = `Fetches content from a URL and returns it in the specified format.

Uses Mozilla Readability to automatically extract main page content, stripping navigation,
footers, ads, and other chrome. Returns only the readable content for text and markdown formats.

WHEN TO USE THIS TOOL:
- Use when you need to download content from a URL
- Helpful for retrieving documentation, API responses, or web content
- Useful for getting external information to assist with tasks

HOW TO USE:
- Provide the URL to fetch content from
- Specify the desired output format (text, markdown, or html)
- Optionally set a timeout for the request

FEATURES:
- Automatic content extraction using Mozilla Readability (strips nav, ads, footers)
- Supports three output formats: text, markdown, and html
- Automatically handles HTTP redirects
- Detects likely JavaScript-rendered pages and warns you
- Sets reasonable timeouts to prevent hanging

PARAMETERS:
- max_size: Maximum bytes to fetch (default: 16000, ~16KB)
  Prevents downloading huge files that could overwhelm context

IMPORTANT LIMITATIONS:
- Cannot render JavaScript. Single-page apps (SPAs) will return little or no content.
  The response metadata will include possible_js_rendered=true when this is detected.
  For JS-heavy sites, consider using browser tools instead.
- Default maximum response size is 16KB (use max_size to adjust)
- Only supports HTTP and HTTPS protocols
- Cannot handle authentication or cookies
- Some websites may block automated requests

TIPS FOR BETTER RESULTS:
- For GitHub repos, use raw.githubusercontent.com URLs instead of github.com
  (e.g., https://raw.githubusercontent.com/org/repo/main/README.md)
- For API docs that are JS-rendered SPAs, look for the OpenAPI/Swagger JSON spec URL instead
- Use text or markdown format for documentation (html returns raw markup with all chrome)
- If the response says possible_js_rendered=true, the page needs JavaScript to render.
  Try finding an alternative URL, a raw content source, or use browser tools.
- Adjust max_size for larger documents (but consider context limits)

RESPONSE METADATA:
- content_length: Size of the extracted content
- raw_html_size: Size of the original HTML before extraction (for HTML pages)
- truncated: Whether content was truncated to fit max_size
- encoding_used: The format that was applied
- page_title: Page title extracted by Readability (when available)
- possible_js_rendered: True if the page appears to be JavaScript-rendered (very little content extracted)
- used_readability: True if Readability content extraction was applied`
)

func NewFetchTool() Tool {
	tool := &fetchTool{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	return NewToolWrapper[FetchParams, ToolResponse](tool)
}

func (t *fetchTool) Name() string {
	return FetchToolName
}

func (t *fetchTool) Description() string {
	return fetchToolDescription
}

func (t *fetchTool) RequiresPermission(params FetchParams) (bool, error) {
	// fetch tool requires permissions as it accesses external resources
	return true, nil
}

func (t *fetchTool) Execute(rctx *rctx.ToolContext, params FetchParams) (ToolResponse, error) {

	if params.URL == "" {
		return NewTextErrorResponse("URL parameter is required"), nil
	}

	format := strings.ToLower(params.Format)
	if format != "text" && format != "markdown" && format != "html" {
		return NewTextErrorResponse("Format must be one of: text, markdown, html"), nil
	}

	if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
		return NewTextErrorResponse("URL must start with http:// or https://"), nil
	}

	client := t.client
	if params.Timeout > 0 {
		maxTimeout := 120 // 2 minutes
		if params.Timeout > maxTimeout {
			params.Timeout = maxTimeout
		}
		client = &http.Client{
			Timeout: time.Duration(params.Timeout) * time.Second,
		}
	}

	req, err := http.NewRequestWithContext(rctx.Context, "GET", params.URL, nil)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; reliant/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logging.Error("Failed to close response body", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return NewTextErrorResponse(fmt.Sprintf("Request failed with status code: %d", resp.StatusCode)), nil
	}

	// Use custom max size if provided, otherwise default to 16KB
	maxSize := int64(16000) // Default 16KB - matches MaxOutputSize
	if params.MaxSize > 0 {
		maxSize = int64(params.MaxSize)
	}

	// Get content length from header
	contentLength := resp.ContentLength
	if contentLength < 0 {
		contentLength = 0 // Unknown size
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSize))
	if err != nil {
		return NewTextErrorResponse("Failed to read response body: " + err.Error()), nil
	}

	// Check if content was truncated
	wasTruncated := int64(len(body)) == maxSize && (contentLength == 0 || contentLength > maxSize)

	rawContent := string(body)
	contentType := resp.Header.Get("Content-Type")
	isHTML := strings.Contains(contentType, "text/html")

	var result string
	var encodingUsed string
	var pageTitle string
	var possibleJSRender bool
	var usedReadability bool
	rawHTMLSize := 0

	if isHTML {
		rawHTMLSize = len(rawContent)

		// Try readability extraction for text and markdown formats
		if format == "text" || format == "markdown" {
			article, readErr := extractWithReadability(rawContent, params.URL)
			if readErr == nil && article.Content != "" {
				usedReadability = true
				pageTitle = article.Title

				if format == "text" {
					encodingUsed = "text"
					result = article.TextContent
				} else {
					// Convert readability HTML to markdown
					encodingUsed = "markdown"
					markdown, mdErr := convertHTMLToMarkdown(article.Content)
					if mdErr != nil {
						// Fall back to text content
						result = article.TextContent
					} else {
						result = markdown
					}
				}

				// Check for JS-rendered page: readability succeeded but content is suspiciously small
				if len(strings.TrimSpace(result)) < 200 && rawHTMLSize > 2000 {
					possibleJSRender = true
				}
			} else {
				// Readability failed or returned empty - fall back to basic extraction
				if format == "text" {
					encodingUsed = "text"
					text, textErr := extractTextFromHTML(rawContent)
					if textErr != nil {
						return NewTextErrorResponse("Failed to extract text from HTML: " + textErr.Error()), nil
					}
					result = text
				} else {
					encodingUsed = "markdown"
					markdown, mdErr := convertHTMLToMarkdown(rawContent)
					if mdErr != nil {
						return NewTextErrorResponse("Failed to convert HTML to Markdown: " + mdErr.Error()), nil
					}
					result = markdown
				}

				// Check for JS-rendered page: very little text extracted from large HTML
				extractedLen := len(strings.TrimSpace(result))
				if extractedLen < 200 && rawHTMLSize > 2000 {
					possibleJSRender = true
				}
			}
		} else {
			// HTML format - return raw content
			encodingUsed = "html"
			result = rawContent
		}
	} else {
		// Non-HTML content
		switch format {
		case "text":
			encodingUsed = "text"
			result = rawContent
		case "markdown":
			encodingUsed = "markdown"
			result = "```\n" + rawContent + "\n```"
		default:
			encodingUsed = format
			result = rawContent
		}
	}

	metadata := FetchResponseMetadata{
		ContentLength:    len(result),
		RawHTMLSize:      rawHTMLSize,
		Truncated:        wasTruncated,
		EncodingUsed:     encodingUsed,
		StatusCode:       resp.StatusCode,
		PageTitle:        pageTitle,
		PossibleJSRender: possibleJSRender,
		UsedReadability:  usedReadability,
	}

	// Append warnings for problematic results
	var warnings []string

	if wasTruncated {
		warnings = append(warnings, fmt.Sprintf("[Content truncated at %d bytes]", maxSize))
	}

	if possibleJSRender {
		warnings = append(warnings, "[WARNING: This page appears to be JavaScript-rendered (very little content extracted from the HTML). "+
			"The page likely requires a browser to render. Try: (1) finding a raw content URL (e.g., raw.githubusercontent.com for GitHub), "+
			"(2) looking for an API endpoint that returns JSON, or (3) using browser tools to render the page.]")
	}

	if len(warnings) > 0 {
		result += "\n\n" + strings.Join(warnings, "\n")
	}

	return WithResponseMetadata(NewTextResponse(result), metadata), nil
}

// extractWithReadability uses Mozilla Readability to extract the main content from HTML.
func extractWithReadability(htmlContent string, pageURL string) (readability.Article, error) {
	parsedURL, err := url.Parse(pageURL)
	if err != nil {
		return readability.Article{}, fmt.Errorf("failed to parse URL: %w", err)
	}

	article, err := readability.FromReader(strings.NewReader(htmlContent), parsedURL)
	if err != nil {
		return readability.Article{}, fmt.Errorf("readability extraction failed: %w", err)
	}

	return article, nil
}

func extractTextFromHTML(html string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}

	// Remove script, style, nav, footer, header elements before extracting text
	doc.Find("script, style, nav, footer, header, aside, .nav, .footer, .header, .sidebar, .menu").Remove()

	text := doc.Text()
	text = strings.Join(strings.Fields(text), " ")

	return text, nil
}

func convertHTMLToMarkdown(html string) (string, error) {
	converter := md.NewConverter("", true, nil)

	markdown, err := converter.ConvertString(html)
	if err != nil {
		return "", err
	}

	return markdown, nil
}
