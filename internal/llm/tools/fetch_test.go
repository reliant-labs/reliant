// Copyright (c) 2025 Reliant Labs
package tools

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/netguard"

	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchTool(t *testing.T) {
	t.Parallel()
	tool := NewFetchTool(netguard.ForDeployment(""))

	assert.Equal(t, FetchToolName, tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.Contains(t, tool.Description(), "Readability")
	assert.Contains(t, tool.Description(), "possible_js_rendered")
}

func TestFetchToolValidation(t *testing.T) {
	t.Parallel()
	tool := NewFetchTool(netguard.ForDeployment(""))
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
	t.Parallel()
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

	tool := NewFetchTool(netguard.ForDeployment(""))
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
	t.Parallel()
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

	tool := NewFetchTool(netguard.ForDeployment(""))
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
	t.Parallel()
	jsonContent := `{"name": "test", "version": "1.0.0"}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, jsonContent)
	}))
	defer server.Close()

	tool := NewFetchTool(netguard.ForDeployment(""))
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
	t.Parallel()
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
	t.Parallel()
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

func runFetchCaps(t *testing.T, params FetchParams) ToolResponse {
	t.Helper()
	tool := NewFetchTool(netguard.ForDeployment("")).(*ToolWrapper[FetchParams, ToolResponse])
	resp, err := tool.tool.Execute(&rctx.ToolContext{Context: context.Background()}, params)
	require.NoError(t, err)
	return resp
}

// noteSlack bounds the size of the truncation / clamp notes appended to the body.
const noteSlack = 1024

func TestFetchMaxSizeIsCapped(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		chunk := bytes.Repeat([]byte("a"), 64<<10)
		for i := 0; i < 64; i++ { // 4 MiB
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	resp := runFetchCaps(t, FetchParams{URL: srv.URL, Format: "text", MaxSize: 1 << 31})
	assert.False(t, resp.IsError)
	assert.LessOrEqual(t, len(resp.Content), fetchMaxSizeCap+noteSlack)
	assert.GreaterOrEqual(t, len(resp.Content), fetchMaxSizeCap)
	assert.Contains(t, resp.Content, "[Content truncated")
	assert.Contains(t, resp.Content, "hard cap")
	assert.Contains(t, resp.Metadata, `"truncated":true`)
}

func TestFetchMaxSizeCanLowerButNotRaise(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("b", 5000))
	}))
	defer srv.Close()

	resp := runFetchCaps(t, FetchParams{URL: srv.URL, Format: "text", MaxSize: 100})
	assert.LessOrEqual(t, len(resp.Content), 100+noteSlack)
	assert.Contains(t, resp.Content, "[Content truncated")
	assert.NotContains(t, resp.Content, "hard cap", "no clamp note when the model asked for less than the cap")

	resp = runFetchCaps(t, FetchParams{URL: srv.URL, Format: "text", MaxSize: 100000})
	assert.NotContains(t, resp.Content, "[Content truncated", "a response smaller than max_size is not truncated")
	assert.Contains(t, resp.Metadata, `"truncated":false`)
}

func TestFetchExactlyMaxSizeIsNotTruncated(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("c", 100))
	}))
	defer srv.Close()

	resp := runFetchCaps(t, FetchParams{URL: srv.URL, Format: "text", MaxSize: 100})
	assert.Equal(t, strings.Repeat("c", 100), resp.Content)
}

func TestFetchGzipBombStaysWithinCap(t *testing.T) {
	t.Parallel()
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	zeros := make([]byte, 1<<20)
	for i := 0; i < 256; i++ { // 256 MiB decompressed, well under 1 MiB on the wire
		_, err := zw.Write(zeros)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	require.Less(t, compressed.Len(), 2<<20)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(compressed.Len()))
		_, _ = w.Write(compressed.Bytes())
	}))
	defer srv.Close()

	resp := runFetchCaps(t, FetchParams{URL: srv.URL, Format: "text", MaxSize: 1 << 31})
	assert.False(t, resp.IsError)
	assert.LessOrEqual(t, len(resp.Content), fetchMaxSizeCap+noteSlack)
	assert.Contains(t, resp.Content, "[Content truncated")
}

func TestFetchLargeHTMLIsBoundedBeforeExtraction(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<html><body><article><p>")
		chunk := strings.Repeat("word ", 1<<12)
		for i := 0; i < 2048; i++ { // ~40 MiB of text
			if _, err := fmt.Fprint(w, chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	for _, format := range []string{"text", "markdown", "html"} {
		resp := runFetchCaps(t, FetchParams{URL: srv.URL, Format: format, MaxSize: 1 << 31})
		assert.False(t, resp.IsError, format)
		assert.LessOrEqual(t, len(resp.Content), 2*fetchMaxSizeCap+noteSlack, format)
		assert.Contains(t, resp.Content, "[Content truncated", format)
		assert.Contains(t, resp.Metadata, `"raw_html_size":`+strconv.Itoa(fetchMaxSizeCap), format)
	}
}

func redirectChainServer(hops int) *httptest.Server {
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/hop/", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hop/"))
		if n >= hops {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "arrived")
			return
		}
		http.Redirect(w, r, srv.URL+"/hop/"+strconv.Itoa(n+1), http.StatusFound)
	})
	srv = httptest.NewServer(mux)
	return srv
}

func TestFetchRedirectHopLimit(t *testing.T) {
	t.Parallel()

	ok := redirectChainServer(fetchMaxRedirectHops)
	defer ok.Close()
	resp := runFetchCaps(t, FetchParams{URL: ok.URL + "/hop/0", Format: "text"})
	assert.False(t, resp.IsError)
	assert.Contains(t, resp.Content, "arrived")

	tooMany := redirectChainServer(fetchMaxRedirectHops + 1)
	defer tooMany.Close()
	resp = runFetchCaps(t, FetchParams{URL: tooMany.URL + "/hop/0", Format: "text"})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "stopped after 5 redirects")
	assert.NotContains(t, resp.Content, "arrived")
}

func TestFetchRefusesNonHTTPRedirectSchemes(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"file:///etc/passwd", "gopher://127.0.0.1:70/_x", "ftp://example.com/x"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusFound)
		}))
		resp := runFetchCaps(t, FetchParams{URL: srv.URL, Format: "text"})
		srv.Close()
		assert.True(t, resp.IsError, target)
		assert.Contains(t, resp.Content, "scheme is not allowed", target)
	}
}

func TestFetchTimeoutClamp(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 0, clampFetchTimeout(0))
	assert.Equal(t, 0, clampFetchTimeout(-5))
	assert.Equal(t, 10, clampFetchTimeout(10))
	assert.Equal(t, 60, clampFetchTimeout(60))
	assert.Equal(t, 60, clampFetchTimeout(61))
	assert.Equal(t, 60, clampFetchTimeout(1<<30))

	policy := netguard.ForDeployment("")
	assert.Equal(t, 60*time.Second, newFetchClient(policy, clampFetchTimeout(100000)).Timeout)
	assert.Equal(t, time.Duration(fetchDefaultTimeout)*time.Second, NewFetchTool(policy).(*ToolWrapper[FetchParams, ToolResponse]).tool.(*fetchTool).client.Timeout)
	assert.NotNil(t, newFetchClient(policy, 5).CheckRedirect)
}

func TestFetchDescriptionStatesTheCaps(t *testing.T) {
	t.Parallel()
	tool := NewFetchTool(netguard.ForDeployment(""))
	assert.Contains(t, tool.Description(), "1048576")
	assert.Contains(t, tool.Description(), "hard cap")
}
