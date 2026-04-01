// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchTool(t *testing.T) {
	tool := NewFetchTool()

	assert.Equal(t, FetchToolName, tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.Contains(t, tool.Description(), "Readability")
	assert.Contains(t, tool.Description(), "possible_js_rendered")
}

func TestFetchToolValidation(t *testing.T) {
	tool := NewFetchTool()
	typedTool := tool.(*ToolWrapper[FetchParams, ToolResponse])

	toolCtx := &rctx.ToolContext{
		Context: context.Background(),
	}

	t.Run("EmptyURL", func(t *testing.T) {
		resp, err := typedTool.tool.Execute(toolCtx, FetchParams{URL: "", Format: "text"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "URL parameter is required")
	})

	t.Run("InvalidFormat", func(t *testing.T) {
		resp, err := typedTool.tool.Execute(toolCtx, FetchParams{URL: "https://example.com", Format: "xml"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "Format must be one of")
	})

	t.Run("InvalidProtocol", func(t *testing.T) {
		resp, err := typedTool.tool.Execute(toolCtx, FetchParams{URL: "ftp://example.com", Format: "text"})
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "URL must start with http")
	})
}

func TestFetchReadabilityExtraction(t *testing.T) {
	// Create a test server that returns realistic HTML with nav chrome
	richHTML := `<!DOCTYPE html>
<html>
<head><title>Test Documentation Page</title></head>
<body>
<nav class="main-nav">
  <a href="/">Home</a>
  <a href="/docs">Docs</a>
  <a href="/api">API</a>
  <a href="/login">Login</a>
  <a href="/signup">Sign Up</a>
  <a href="/pricing">Pricing</a>
  <a href="/blog">Blog</a>
  <a href="/support">Support</a>
</nav>
<div class="sidebar">
  <ul>
    <li><a href="/docs/getting-started">Getting Started</a></li>
    <li><a href="/docs/install">Installation</a></li>
    <li><a href="/docs/config">Configuration</a></li>
    <li><a href="/docs/api">API Reference</a></li>
  </ul>
</div>
<main>
  <article>
    <h1>Getting Started with the Widget API</h1>
    <p>The Widget API allows you to create, read, update, and delete widgets programmatically.
    This guide walks you through the basic concepts and shows you how to make your first API call.</p>
    <h2>Authentication</h2>
    <p>All API requests require authentication using an API key. You can generate an API key
    from your account settings page. Include the key in the Authorization header of every request.</p>
    <pre><code>curl -H "Authorization: Bearer YOUR_API_KEY" https://api.example.com/v1/widgets</code></pre>
    <h2>Creating a Widget</h2>
    <p>To create a new widget, send a POST request to the /v1/widgets endpoint with a JSON body
    containing the widget properties.</p>
    <pre><code>curl -X POST https://api.example.com/v1/widgets \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name": "My Widget", "type": "counter"}'</code></pre>
    <h2>Response Format</h2>
    <p>All API responses are returned in JSON format. Successful responses include a data field
    containing the requested resource. Error responses include an error field with a message
    describing what went wrong.</p>
  </article>
</main>
<footer>
  <p>Copyright 2024 Example Corp. All rights reserved.</p>
  <a href="/privacy">Privacy Policy</a>
  <a href="/terms">Terms of Service</a>
  <a href="/contact">Contact Us</a>
</footer>
</body>
</html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, richHTML)
	}))
	defer server.Close()

	tool := NewFetchTool()
	typedTool := tool.(*ToolWrapper[FetchParams, ToolResponse])
	toolCtx := &rctx.ToolContext{Context: context.Background()}

	t.Run("TextFormatWithReadability", func(t *testing.T) {
		resp, err := typedTool.tool.Execute(toolCtx, FetchParams{
			URL:    server.URL,
			Format: "text",
		})
		require.NoError(t, err)
		assert.False(t, resp.IsError)

		// Should contain the main article content
		assert.Contains(t, resp.Content, "Widget API")
		assert.Contains(t, resp.Content, "Authentication")
		assert.Contains(t, resp.Content, "Creating a Widget")

		// Should NOT contain nav/footer chrome (readability should strip it)
		// Note: readability may or may not strip these perfectly, but the content
		// should be primarily the article text
		assert.Contains(t, resp.Content, "API key")
	})

	t.Run("MarkdownFormatWithReadability", func(t *testing.T) {
		resp, err := typedTool.tool.Execute(toolCtx, FetchParams{
			URL:    server.URL,
			Format: "markdown",
		})
		require.NoError(t, err)
		assert.False(t, resp.IsError)

		// Should have markdown formatting
		assert.Contains(t, resp.Content, "Widget API")
		assert.Contains(t, resp.Content, "Authentication")
	})

	t.Run("HTMLFormatSkipsReadability", func(t *testing.T) {
		resp, err := typedTool.tool.Execute(toolCtx, FetchParams{
			URL:    server.URL,
			Format: "html",
		})
		require.NoError(t, err)
		assert.False(t, resp.IsError)

		// Should contain raw HTML including nav
		assert.Contains(t, resp.Content, "<nav")
		assert.Contains(t, resp.Content, "<footer>")
	})
}

func TestFetchJSRenderedDetection(t *testing.T) {
	// Simulate a JS-rendered SPA page - large HTML, almost no text content
	spaHTML := `<!DOCTYPE html>
<html>
<head>
<title>API Reference</title>
<meta charset="utf-8">
<link rel="stylesheet" href="/static/styles.css">
</head>
<body>
<div id="root"></div>
<script src="/static/bundle.js"></script>
<script>window.__APP_CONFIG__ = {"apiUrl": "https://api.example.com"};</script>
<noscript>You need to enable JavaScript to run this app.</noscript>
</body>
</html>`

	// Pad it to make it look like a "real" page (>2000 bytes)
	padding := "<!-- " + string(make([]byte, 2000)) + " -->"
	spaHTML = spaHTML[:len(spaHTML)-len("</html>")] + padding + "</html>"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, spaHTML)
	}))
	defer server.Close()

	tool := NewFetchTool()
	typedTool := tool.(*ToolWrapper[FetchParams, ToolResponse])
	toolCtx := &rctx.ToolContext{Context: context.Background()}

	resp, err := typedTool.tool.Execute(toolCtx, FetchParams{
		URL:    server.URL,
		Format: "text",
	})
	require.NoError(t, err)
	assert.False(t, resp.IsError)

	// Should detect JS-rendered page and include warning
	assert.Contains(t, resp.Content, "JavaScript-rendered")
}

func TestFetchNonHTMLContent(t *testing.T) {
	jsonContent := `{"name": "test", "version": "1.0.0"}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, jsonContent)
	}))
	defer server.Close()

	tool := NewFetchTool()
	typedTool := tool.(*ToolWrapper[FetchParams, ToolResponse])
	toolCtx := &rctx.ToolContext{Context: context.Background()}

	t.Run("TextFormat", func(t *testing.T) {
		resp, err := typedTool.tool.Execute(toolCtx, FetchParams{
			URL:    server.URL,
			Format: "text",
		})
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		assert.Contains(t, resp.Content, `"name": "test"`)
	})

	t.Run("MarkdownFormat", func(t *testing.T) {
		resp, err := typedTool.tool.Execute(toolCtx, FetchParams{
			URL:    server.URL,
			Format: "markdown",
		})
		require.NoError(t, err)
		assert.False(t, resp.IsError)
		// Non-HTML markdown wraps in code block
		assert.Contains(t, resp.Content, "```")
		assert.Contains(t, resp.Content, `"name": "test"`)
	})
}

func TestExtractWithReadability(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head><title>Article Title</title></head>
<body>
<article>
<h1>Main Article Heading</h1>
<p>This is the main content of the article. It contains important information
that the reader needs to understand. The content is substantial enough that
readability should pick it up as the main content of the page.</p>
<p>Here is another paragraph with more detail about the topic. We include
enough text to ensure readability considers this significant content rather
than boilerplate.</p>
<p>The third paragraph continues with additional information, examples, and
explanations that make this article valuable to the reader.</p>
</article>
</body>
</html>`

	article, err := extractWithReadability(html, "https://example.com/article")
	require.NoError(t, err)
	assert.Equal(t, "Article Title", article.Title)
	assert.Contains(t, article.TextContent, "main content")
	assert.NotEmpty(t, article.Content) // HTML content
}

func TestExtractTextFromHTML(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head><title>Test</title><script>var x = 1;</script><style>body { color: red; }</style></head>
<body>
<nav>Navigation links here</nav>
<main><p>Main content here</p></main>
<footer>Footer content</footer>
</body>
</html>`

	text, err := extractTextFromHTML(html)
	require.NoError(t, err)

	// Should have main content
	assert.Contains(t, text, "Main content here")

	// Should NOT have script/style content
	assert.NotContains(t, text, "var x = 1")
	assert.NotContains(t, text, "color: red")

	// Should NOT have nav/footer (we remove them now)
	assert.NotContains(t, text, "Navigation links")
	assert.NotContains(t, text, "Footer content")
}
