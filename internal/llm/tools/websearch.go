// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

type WebSearchParams struct {
	Query string `json:"query" jsonschema:"required,description=The search query to execute"`
	Count int    `json:"count,omitempty" jsonschema:"description=Number of results to return (default: 10 max: 20)"`
}

type searchResult struct {
	Title       string
	URL         string
	Description string
}

type WebSearchResponseMetadata struct {
	NumberOfResults int    `json:"number_of_results"`
	Query           string `json:"query"`
	Source          string `json:"source"`
}

type webSearchTool struct {
	client *http.Client
}

const (
	WebSearchToolName        = "websearch"
	webSearchToolDescription = `Search the web using DuckDuckGo's HTML search.

WHEN TO USE THIS TOOL:
- Finding current information not available in the assistant's training data
- Researching documentation, tutorials, or examples online
- Looking up error messages or debugging information
- Finding libraries, tools, or frameworks
- Checking current status of services or projects
- Discovering recent developments or news about technologies

HOW TO USE:
- Provide a search query as you would in a web browser
- Optionally specify the number of results (default: 10, max: 20)

QUERY GUIDELINES:
- Use simple, natural language queries with specific keywords
- Keep queries short and focused (3-8 words works best)
- Use minus (-) to exclude terms: python tutorial -django
- Quotes work for simple exact phrases: "react hooks" tutorial

IMPORTANT - UNSUPPORTED QUERY SYNTAX:
DuckDuckGo's HTML search does NOT support these features (they will return zero results):
- Boolean operators: OR, AND (e.g. "PUT" OR "POST" will FAIL)
- Complex quoted phrase combinations (multiple quoted phrases with operators)
- site: operator is unreliable and often returns no results
- filetype: operator is unreliable
If you need boolean-style searches, run multiple simple queries instead.

EXAMPLES OF GOOD QUERIES:
- "golang context timeout example" - Simple keywords
- "anthropic claude api documentation" - Natural language
- "Customer.io transactional API" - Product + feature
- "react hooks tutorial 2024" - Topic + timeframe

EXAMPLES OF BAD QUERIES (will return 0 results):
- "PUT" OR "POST" OR "PATCH" email content - Boolean operators don't work
- site:customer.io/docs/api specific-page - site: is unreliable
- "exact phrase 1" OR "exact phrase 2" - Complex boolean combos fail

RESEARCH STRATEGY:
- Start with broad queries, then narrow based on results
- If a search returns 0 results, SIMPLIFY the query - don't add complexity
- After 3-4 searches on the same topic, synthesize what you have rather than keep searching
- For API documentation, search for the official SDK/client library on GitHub instead
- For GitHub content, prefer fetching raw.githubusercontent.com URLs over github.com

RESPONSE FORMAT:
Returns a list of search results with:
- Title: The title of the search result
- Description: A brief description/snippet from the page
- URL: The link to the resource

LIMITATIONS:
- Maximum of 20 results per query
- May have rate limits if used excessively
- DuckDuckGo HTML search has limited query syntax (see above)
- Results may be less comprehensive than Google for niche technical queries

TIPS:
- Start with fewer results (5-10) for faster responses
- Use specific queries to get more relevant results
- Combine with the fetch tool to retrieve full page content from results`
)

func NewWebSearchTool() Tool {
	tool := &webSearchTool{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	return NewToolWrapper[WebSearchParams, ToolResponse](tool)
}

func (t *webSearchTool) Name() string {
	return WebSearchToolName
}

func (t *webSearchTool) Description() string {
	return webSearchToolDescription
}

func (t *webSearchTool) RequiresPermission(params WebSearchParams) (bool, error) {
	// web search requires permissions as it accesses external services
	return true, nil
}

func (t *webSearchTool) Execute(rctx *rctx.ToolContext, params WebSearchParams) (ToolResponse, error) {
	if params.Query == "" {
		return NewTextErrorResponse("Query parameter is required"), nil
	}

	// Set default count
	if params.Count <= 0 {
		params.Count = 10
	} else if params.Count > 20 {
		params.Count = 20 // Limit to 20 results
	}

	// Check for problematic query patterns
	queryWarnings := validateSearchQuery(params.Query)

	// Perform the search using DuckDuckGo HTML
	results, err := t.searchDuckDuckGo(rctx, params.Query)
	if err != nil {
		return ToolResponse{}, fmt.Errorf("search failed: %w", err)
	}

	// Limit results to requested count
	if len(results) > params.Count {
		results = results[:params.Count]
	}

	// Format results
	formattedResults := formatWebSearchResults(results, params)

	// Prepend query warnings if any
	if len(queryWarnings) > 0 {
		var warningBuf strings.Builder
		warningBuf.WriteString("**QUERY WARNINGS:**\n")
		for _, w := range queryWarnings {
			warningBuf.WriteString(fmt.Sprintf("- %s\n", w))
		}
		warningBuf.WriteString("\n")
		formattedResults = warningBuf.String() + formattedResults
	}

	// Append guidance on zero results
	if len(results) == 0 {
		formattedResults += "\n**SUGGESTION:** Your query returned no results. Try simplifying: use fewer keywords, remove quotes and boolean operators (OR/AND), and use natural language. DuckDuckGo HTML search works best with simple keyword queries."
	}

	metadata := WebSearchResponseMetadata{
		NumberOfResults: len(results),
		Query:           params.Query,
		Source:          "DuckDuckGo",
	}

	return WithResponseMetadata(NewTextResponse(formattedResults), metadata), nil
}

// booleanOpPattern matches standalone boolean operators like OR, AND in queries
var booleanOpPattern = regexp.MustCompile(`\b(OR|AND)\b`)

// validateSearchQuery checks for query patterns that DuckDuckGo HTML search handles poorly
// and returns warning messages for each detected issue.
func validateSearchQuery(query string) []string {
	var warnings []string

	// Check for boolean operators (OR, AND)
	if booleanOpPattern.MatchString(query) {
		warnings = append(warnings, "Query contains boolean operators (OR/AND) which DuckDuckGo HTML search does not support. These will likely cause zero results. Run multiple simple queries instead.")
	}

	// Check for excessive quoted phrases (3+ quoted segments)
	quotedPhrases := strings.Count(query, `"`)
	if quotedPhrases >= 6 { // 3+ quoted phrases = 6+ quote characters
		warnings = append(warnings, "Query contains many quoted phrases. DuckDuckGo works best with at most one quoted phrase. Simplify the query.")
	}

	// Check for site: operator
	if strings.Contains(query, "site:") {
		warnings = append(warnings, "The site: operator is unreliable with DuckDuckGo HTML search and often returns no results. Try searching without it and filter results manually.")
	}

	// Check for very long queries (>120 chars tend to fail)
	if len(query) > 120 {
		warnings = append(warnings, "Query is very long (>120 chars). DuckDuckGo works best with short, focused queries (3-8 words). Consider simplifying.")
	}

	return warnings
}

func (t *webSearchTool) searchDuckDuckGo(rctx *rctx.ToolContext, query string) ([]searchResult, error) {
	// Use DuckDuckGo's HTML search endpoint
	searchURL := fmt.Sprintf("https://html.duckduckgo.com/html/?q=%s", url.QueryEscape(query))

	req, err := http.NewRequestWithContext(rctx.Context, "GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers to mimic a browser
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; reliant/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute search: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logging.Error("Failed to close response body", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if len(body) > 500 {
			body = body[:500]
		}
		return nil, fmt.Errorf("search failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse the HTML response
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	var results []searchResult

	// DuckDuckGo HTML results are in div.result elements
	doc.Find(".result").Each(func(i int, s *goquery.Selection) {
		// Extract title and link from the result__a element
		titleElement := s.Find(".result__a")
		title := strings.TrimSpace(titleElement.Text())
		href, exists := titleElement.Attr("href")

		// Extract snippet from result__snippet
		snippet := strings.TrimSpace(s.Find(".result__snippet").Text())

		if title != "" && exists && href != "" {
			// DuckDuckGo uses redirect URLs, extract the actual URL
			actualURL := extractActualURL(href)
			results = append(results, searchResult{
				Title:       title,
				URL:         actualURL,
				Description: snippet,
			})
		}
	})

	return results, nil
}

// extractActualURL extracts the actual destination URL from DuckDuckGo's redirect URL
func extractActualURL(ddgURL string) string {
	// DuckDuckGo uses URLs like: //duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2F&rut=...
	if strings.HasPrefix(ddgURL, "//duckduckgo.com/l/") {
		parsed, err := url.Parse("https:" + ddgURL)
		if err == nil {
			if uddg := parsed.Query().Get("uddg"); uddg != "" {
				return uddg
			}
		}
	}
	return ddgURL
}

func formatWebSearchResults(results []searchResult, params WebSearchParams) string {
	var buffer strings.Builder

	buffer.WriteString("# Web Search Results\n\n")
	buffer.WriteString(fmt.Sprintf("Query: **%s**\n", params.Query))
	buffer.WriteString("Source: DuckDuckGo\n")
	buffer.WriteString(fmt.Sprintf("Results: %d\n\n", len(results)))

	if len(results) == 0 {
		buffer.WriteString("No results found. Try a different query.\n")
		return buffer.String()
	}

	buffer.WriteString("---\n\n")

	for i, result := range results {
		buffer.WriteString(fmt.Sprintf("## %d. %s\n\n", i+1, result.Title))

		if result.URL != "" {
			buffer.WriteString(fmt.Sprintf("**URL:** %s\n\n", result.URL))
		}

		if result.Description != "" {
			buffer.WriteString(fmt.Sprintf("%s\n\n", result.Description))
		}

		buffer.WriteString("---\n\n")
	}

	return buffer.String()
}
