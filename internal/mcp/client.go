// Copyright (c) 2025 Reliant Labs
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
//
//forge:lint-disable-next-line forge-exclude-contract-multi-impl: mcp.Client is already the seam (direct, lazy, daemon-proxied); moving it to contract.go is deferred with the MCP package split; tracked in H-RELIANT-CI-lint follow-ups
//forge:exclude-contract: MCP protocol client (stdio/HTTP) and its lazy and daemon-proxied variants
package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/reliant/internal/cmdutil"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp/catalog"
	"github.com/reliant-labs/reliant/internal/mcp/compat"
	mcpruntime "github.com/reliant-labs/reliant/internal/mcp/runtime"
	mcpservice "github.com/reliant-labs/reliant/internal/mcp/service"
)

// client implements the MCP Client interface using the official SDK
type client struct {
	name       string
	cfg        config.MCPServer
	sdkClient  *mcp.Client
	session    mcpruntime.Session
	transport  mcp.Transport
	serverInfo *ServerInfo

	mu              sync.Mutex
	initialized     bool
	toolCallService *mcpservice.ToolCallService
	mcpService      *mcpservice.MCPService

	preferredEnvelopeByTool map[string]string

	// lookupEnv resolves the ${VAR} references in cfg at launch (expand.go).
	// Never nil: newClientWithEnvLookup substitutes resolveNoEnv for nil.
	lookupEnv func(string) (string, bool)
}

// NewClient creates a standalone MCP client, resolving references from the
// caller's environment as before. Use NewManager for role-scoped clients;
// a server managing tenant configuration must not use this standalone path.
func NewClient(name string, cfg config.MCPServer) (Client, error) {
	return newClientWithEnvLookup(name, cfg, os.LookupEnv) //nolint:forbidigo // legacy standalone client runs in the caller's environment
}

// newClientWithEnvLookup resolves references with the supplied policy.
// nil means no process lookup: each reference uses its default or is empty.
func newClientWithEnvLookup(name string, cfg config.MCPServer, lookupEnv func(string) (string, bool)) (Client, error) {
	if lookupEnv == nil {
		lookupEnv = resolveNoEnv
	}
	// Create SDK client implementation
	impl := &mcp.Implementation{
		Name:    "reliant",
		Version: "1.0.0",
	}

	// Create SDK client with no special options for now
	sdkClient := mcp.NewClient(impl, nil)

	return &client{
		name:                    name,
		cfg:                     cfg,
		sdkClient:               sdkClient,
		toolCallService:         mcpservice.NewToolCallService(nil),
		mcpService:              mcpservice.NewMCPService(nil),
		preferredEnvelopeByTool: make(map[string]string),
		lookupEnv:               lookupEnv,
	}, nil
}

// Initialize performs the MCP handshake and initialization.
// The provided context is used for timeout/cancellation during connection.
// If the context has no deadline, a default 30-second timeout is applied.
func (c *client) Initialize(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.initialized {
		return nil
	}

	logging.DebugContext(ctx, "Initializing MCP client", "name", c.name)

	// Create the appropriate transport based on config type
	var transport mcp.Transport
	var err error
	var stdioCmd *exec.Cmd

	// The config arrives with its ${VAR} references as the user wrote them;
	// this is where they are resolved, through the lookup the client was
	// built with (the daemon's environment on a daemon, nothing on a server).
	// See expand.go.
	launch := resolveLaunchConfig(c.cfg, c.lookupEnv)

	switch launch.Type {
	case config.MCPStdio:
		if launch.Command == "" {
			return fmt.Errorf("stdio transport requires command")
		}

		// The project path was substituted into args (expandArgs) for servers
		// that take the tree as an argument rather than reading their working
		// directory. It is resolved by the manager into cfg before construction,
		// so a client spawned directly (tests, one-off CLI use) simply gets no
		// substitution.
		cmdArgs := append([]string(nil), launch.Args...)
		if launch.Command == "uvx" && !hasArg(cmdArgs, "--native-tls") {
			// Prefer uv's native-tls mode to use OS trust roots on managed/corporate machines.
			cmdArgs = append([]string{"--native-tls"}, cmdArgs...)
		}

		// Resolve command via login shell so nvm/non-standard PATH entries are found.
		finder := cmdutil.NewCommandFinder(launch.Command)
		resolvedCmd, err := finder.Find()
		if err != nil {
			resolvedCmd = launch.Command // fall back to original
		}

		// Create command with environment variables
		cmd := exec.Command(resolvedCmd, cmdArgs...)
		env := os.Environ() //nolint:forbidigo // an MCP server inherits the daemon environment; not configuration
		env = append(env, launch.Env...)

		env = sanitizeMCPSubprocessEnv(env)

		// uvx defaults to a bundled trust store which can miss enterprise/root CAs.
		// Prefer native OS trust unless caller explicitly set UV_NATIVE_TLS.
		if launch.Command == "uvx" && !hasEnvKey(env, "UV_NATIVE_TLS") {
			env = append(env, "UV_NATIVE_TLS=1")
		}

		// Start the process in its resolved directory. Unset until now, so every
		// stdio server inherited the DAEMON's directory — which silently gave a
		// tree-indexing server the wrong tree (see dirscope.go).
		if dir := normalizeProjectPath(launch.Dir); dir != "" {
			if st, err := os.Stat(dir); err == nil && st.IsDir() {
				cmd.Dir = dir
			} else {
				// A missing directory must not take the server down: inheriting
				// the daemon's cwd is the pre-existing behaviour, and a warning
				// is recoverable where a failed handshake is not.
				logging.WarnContext(ctx, "MCP server dir does not exist; inheriting daemon working directory",
					"name", c.name, "dir", dir)
			}
		}

		cmd.Env = env
		stdioCmd = cmd
		transport = &mcp.CommandTransport{Command: cmd}

	case config.MCPSse, config.MCPHTTP:
		if launch.URL == "" {
			return fmt.Errorf("HTTP transport requires URL")
		}
		// Custom headers ride on a transport under the tracing one: the SDK's
		// StreamableClientTransport takes only an *http.Client.
		var base http.RoundTripper
		if len(launch.Headers) > 0 {
			base = &headerTransport{
				base:    http.DefaultTransport,
				headers: launch.Headers,
			}
		}
		httpClient := observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 60 * time.Second, Base: base})

		transport = &mcp.StreamableClientTransport{
			Endpoint:   launch.URL,
			HTTPClient: httpClient,
		}

	default:
		return fmt.Errorf("unknown transport type: %s", launch.Type)
	}

	c.transport = transport

	// Apply default timeout if context has no deadline
	connectCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		connectCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	session, err := c.sdkClient.Connect(connectCtx, transport, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to MCP server: %w", err)
	}

	c.session = mcpruntime.WrapSDKSession(session)
	if stdioCmd != nil {
		go c.watchProcessExit(session, c.session, stdioCmd)
	}

	// Get server info from the session's InitializeResult
	// The SDK handles the initialize handshake automatically
	// We need to extract server info from the session
	// For now, we'll create a minimal ServerInfo
	// TODO: The SDK may expose server info differently
	c.serverInfo = &ServerInfo{
		Name:    c.name,
		Version: "unknown",
		Capabilities: ServerCapabilities{
			Tools: &ToolsCapability{},
		},
	}

	c.initialized = true
	logging.DebugContext(ctx, "MCP client initialized successfully", "name", c.name)

	return nil
}

// ListTools returns all available tools from the MCP server
func (c *client) ListTools() ([]Tool, error) {
	if !c.initialized {
		if err := c.Initialize(context.Background()); err != nil {
			return nil, err
		}
	}

	c.mu.Lock()
	session := c.session
	c.mu.Unlock()

	if session == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Call SDK's ListTools method
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.mcpService.ListTools(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to list tools: %w", err)
	}

	toolDefs := catalog.NormalizeTools(result.Tools)
	tools := make([]Tool, 0, len(toolDefs))
	for _, td := range toolDefs {
		tools = append(tools, Tool{
			Name:        td.Name,
			Description: td.Description,
			InputSchema: td.InputSchema,
		})
	}

	logging.Debug("Listed MCP tools", "name", c.name, "count", len(tools))
	return tools, nil
}

// CallTool executes a tool and returns the result
func (c *client) CallTool(name string, arguments map[string]interface{}) (*ToolResult, error) {
	if !c.initialized {
		if err := c.Initialize(context.Background()); err != nil {
			return nil, err
		}
	}

	c.mu.Lock()
	session := c.session
	c.mu.Unlock()

	if session == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := c.mcpService.CallTool(ctx, session, c.name, name, normalizeToolArguments(arguments))
	if err != nil {
		return nil, err
	}

	// Convert SDK result to our ToolResult type
	toolResult := &ToolResult{
		IsError: result.IsError,
		Content: make([]ToolContent, 0, len(result.Content)),
	}

	for _, content := range result.Content {
		tc := ToolContent{}

		// Handle different content types
		switch v := content.(type) {
		case *mcp.TextContent:
			tc.Type = "text"
			tc.Text = v.Text
		case *mcp.ImageContent:
			tc.Type = "image"
			// Convert []byte to base64 string
			tc.Data = base64.StdEncoding.EncodeToString(v.Data)
			tc.MimeType = v.MIMEType
		default:
			// Unknown content type, try to extract text if available
			tc.Type = "text"
			tc.Text = fmt.Sprintf("%v", content)
		}

		toolResult.Content = append(toolResult.Content, tc)
	}

	return toolResult, nil
}

type toolCaller interface {
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
}

func callToolWithCompatibility(ctx context.Context, caller toolCaller, svc *mcpservice.ToolCallService, serverName, toolName string, arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	if svc == nil {
		svc = mcpservice.NewToolCallService(nil)
	}

	result, _, err := svc.CallToolWithCompatibility(ctx, caller, compat.CallRequest{
		ServerName: serverName,
		ToolName:   toolName,
		Arguments:  normalizeToolArguments(arguments),
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

func normalizeToolArguments(arguments map[string]interface{}) map[string]interface{} {
	if arguments == nil {
		return map[string]interface{}{}
	}
	return arguments
}

// ListResources returns available resources (optional MCP feature)
func (c *client) ListResources() ([]Resource, error) {
	if !c.initialized {
		if err := c.Initialize(context.Background()); err != nil {
			return nil, err
		}
	}

	c.mu.Lock()
	session := c.session
	c.mu.Unlock()

	if session == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.mcpService.ListResources(ctx, session)
	if err != nil {
		// Resources might not be supported
		return []Resource{}, nil
	}

	// Convert SDK resources to our Resource type
	resources := make([]Resource, 0, len(result.Resources))
	for _, sdkRes := range result.Resources {
		res := Resource{
			URI:         sdkRes.URI,
			Name:        sdkRes.Name,
			Description: sdkRes.Description,
			MimeType:    sdkRes.MIMEType,
		}
		resources = append(resources, res)
	}

	return resources, nil
}

// ReadResource reads a specific resource (optional MCP feature)
func (c *client) ReadResource(uri string) (*ResourceContent, error) {
	if !c.initialized {
		if err := c.Initialize(context.Background()); err != nil {
			return nil, err
		}
	}

	c.mu.Lock()
	session := c.session
	c.mu.Unlock()

	if session == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.mcpService.ReadResource(ctx, session, uri)
	if err != nil {
		return nil, fmt.Errorf("failed to read resource %s: %w", uri, err)
	}

	// Convert SDK result to our ResourceContent type
	content := &ResourceContent{
		URI:     uri,
		Content: make([]ToolContent, 0, len(result.Contents)),
	}

	for _, sdkContent := range result.Contents {
		tc := ToolContent{}

		// ResourceContents has Text, Blob, and MIMEType fields (not pointers)
		if sdkContent.Text != "" {
			tc.Type = "text"
			tc.Text = sdkContent.Text
		} else if len(sdkContent.Blob) > 0 {
			tc.Type = "blob"
			// Convert []byte to base64 string
			tc.Data = base64.StdEncoding.EncodeToString(sdkContent.Blob)
		}

		tc.MimeType = sdkContent.MIMEType

		content.Content = append(content.Content, tc)
	}

	return content, nil
}

// ListPrompts returns available prompts (optional MCP feature)
func (c *client) ListPrompts() ([]Prompt, error) {
	if !c.initialized {
		if err := c.Initialize(context.Background()); err != nil {
			return nil, err
		}
	}

	c.mu.Lock()
	session := c.session
	c.mu.Unlock()

	if session == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.mcpService.ListPrompts(ctx, session)
	if err != nil {
		// Prompts might not be supported
		return []Prompt{}, nil
	}

	// Convert SDK prompts to our Prompt type
	prompts := make([]Prompt, 0, len(result.Prompts))
	for _, sdkPrompt := range result.Prompts {
		prompt := Prompt{
			Name:        sdkPrompt.Name,
			Description: sdkPrompt.Description,
			Arguments:   make([]PromptArgument, 0, len(sdkPrompt.Arguments)),
		}

		for _, arg := range sdkPrompt.Arguments {
			prompt.Arguments = append(prompt.Arguments, PromptArgument{
				Name:        arg.Name,
				Description: arg.Description,
				Required:    arg.Required,
			})
		}

		prompts = append(prompts, prompt)
	}

	return prompts, nil
}

// GetPrompt retrieves a specific prompt (optional MCP feature)
func (c *client) GetPrompt(name string, arguments map[string]interface{}) (*PromptResult, error) {
	if !c.initialized {
		if err := c.Initialize(context.Background()); err != nil {
			return nil, err
		}
	}

	c.mu.Lock()
	session := c.session
	c.mu.Unlock()

	if session == nil {
		return nil, fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := c.mcpService.GetPrompt(ctx, session, name, arguments)
	if err != nil {
		return nil, fmt.Errorf("failed to get prompt %s: %w", name, err)
	}

	// Convert SDK result to our PromptResult type
	promptResult := &PromptResult{
		Description: result.Description,
		Messages:    make([]PromptMessage, 0, len(result.Messages)),
	}

	for _, msg := range result.Messages {
		// Content is a single interface value, not a slice
		promptMsg := PromptMessage{
			Role:    string(msg.Role),
			Content: make([]ToolContent, 0, 1),
		}

		tc := ToolContent{}

		// Handle different content types
		switch v := msg.Content.(type) {
		case *mcp.TextContent:
			tc.Type = "text"
			tc.Text = v.Text
		case *mcp.ImageContent:
			tc.Type = "image"
			// Convert []byte to base64 string
			tc.Data = base64.StdEncoding.EncodeToString(v.Data)
			tc.MimeType = v.MIMEType
		default:
			// Unknown content type
			tc.Type = "text"
			tc.Text = fmt.Sprintf("%v", msg.Content)
		}

		promptMsg.Content = append(promptMsg.Content, tc)
		promptResult.Messages = append(promptResult.Messages, promptMsg)
	}

	return promptResult, nil
}

// watchProcessExit waits for a stdio server's connection to end and, when it
// ended without Close — the process exited, crashed or was killed — logs how,
// and marks the client disconnected so the health monitor and lazy wrapper see
// it rather than every later call failing against a closed pipe. Before this a
// server's death left no trace in the log at all (#2818).
//
// Close holds c.mu across session.Close and clears c.session, so a watcher
// woken by Close finds the session it watches already gone and stays quiet.
func (c *client) watchProcessExit(session *mcp.ClientSession, watched mcpruntime.Session, cmd *exec.Cmd) {
	waitErr := session.Wait()

	c.mu.Lock()
	unexpected := c.session == watched
	if unexpected {
		c.session = nil
		c.initialized = false
	}
	c.mu.Unlock()
	if !unexpected {
		return
	}

	attrs := []any{"name", c.name, "exit", describeProcessExit(waitErr)}
	if cmd.Process != nil {
		attrs = append(attrs, "pid", cmd.Process.Pid)
	}
	logging.Warn("MCP server process exited unexpectedly", attrs...)
}

// describeProcessExit renders how a stdio server's connection ended. The SDK
// reports the process's own exit (exit status or signal) as the connection's
// close error once stdout reaches EOF.
func describeProcessExit(err error) string {
	if err == nil {
		return "exit status 0 or stdout closed"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ProcessState != nil {
		return exitErr.String()
	}
	return err.Error()
}

// Close cleanly shuts down the client
func (c *client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.initialized {
		return nil
	}

	logging.Debug("Closing MCP client", "name", c.name)

	// Cleared before closing so the exit watcher, woken by this close, knows
	// it was asked for. A session whose close failed is not reusable either.
	session := c.session
	c.initialized = false
	c.session = nil

	if session != nil {
		if err := session.Close(); err != nil {
			logging.Warn("Error closing MCP session", "name", c.name, "error", err)
			return err
		}
	}

	return nil
}

// IsConnected returns true if the client is connected
func (c *client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initialized && c.session != nil
}

// ServerInfo returns information about the connected server
func (c *client) ServerInfo() *ServerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverInfo
}

// headerTransport is an http.RoundTripper that adds custom headers to requests.
// The values are sent verbatim: their ${VAR} references were resolved at launch.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func hasArg(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}

func sanitizeMCPSubprocessEnv(env []string) []string {
	if !shouldSanitizeProxyEnv(env) {
		return env
	}

	proxyKeys := map[string]struct{}{
		"HTTP_PROXY":              {},
		"HTTPS_PROXY":             {},
		"NO_PROXY":                {},
		"ALL_PROXY":               {},
		"http_proxy":              {},
		"https_proxy":             {},
		"no_proxy":                {},
		"all_proxy":               {},
		"CGI_HTTP_PROXY":          {},
		"SSL_CERT_FILE":           {},
		"SSL_CERT_DIR":            {},
		"REQUESTS_CA_BUNDLE":      {},
		"CURL_CA_BUNDLE":          {},
		"NODE_EXTRA_CA_CERTS":     {},
		"GIT_SSL_CAINFO":          {},
		"CARGO_HTTP_CAINFO":       {},
		"PERL_LWP_SSL_CA_FILE":    {},
		"GLOBAL_AGENT_HTTP_PROXY": {},
		"GLOBAL_AGENT_NO_PROXY":   {},
	}

	sanitized := make([]string, 0, len(env))
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			sanitized = append(sanitized, entry)
			continue
		}

		key := parts[0]
		val := parts[1]
		if _, drop := proxyKeys[key]; drop {
			continue
		}

		if key == "PATH" {
			cleanPath := stripProxymanPathEntries(val)
			sanitized = append(sanitized, key+"="+cleanPath)
			continue
		}

		upperKey := strings.ToUpper(key)
		if strings.Contains(upperKey, "PROXYMAN") {
			continue
		}

		if (key == "PYTHONPATH" || key == "PYTHONHOME") && strings.Contains(strings.ToLower(val), "proxyman") {
			continue
		}

		sanitized = append(sanitized, entry)
	}

	logging.Debug("Sanitized MCP subprocess environment to bypass local proxy/TLS interception for child process", "original_env_count", len(env), "sanitized_env_count", len(sanitized))
	return sanitized
}

func shouldSanitizeProxyEnv(env []string) bool {
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := parts[0]
		val := strings.TrimSpace(parts[1])
		if val == "" {
			continue
		}

		upperKey := strings.ToUpper(key)
		if strings.Contains(upperKey, "PROXYMAN") {
			return true
		}

		switch upperKey {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "GLOBAL_AGENT_HTTP_PROXY", "CGI_HTTP_PROXY":
			if isLoopbackProxy(val) {
				return true
			}
		case "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "PYTHONPATH", "PYTHONHOME":
			if strings.Contains(strings.ToLower(val), "proxyman") {
				return true
			}
		}
	}

	return false
}

func isLoopbackProxy(value string) bool {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return false
	}

	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return strings.Contains(strings.ToLower(value), "localhost") || strings.Contains(value, "127.0.0.1") || strings.Contains(value, "[::1]")
	}

	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func stripProxymanPathEntries(pathValue string) string {
	if pathValue == "" {
		return pathValue
	}

	parts := strings.Split(pathValue, ":")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.Contains(strings.ToLower(part), "proxyman") {
			continue
		}
		if strings.Contains(part, "/Resources/overrides/path") {
			continue
		}
		kept = append(kept, part)
	}

	return strings.Join(kept, ":")
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid modifying the original
	req = req.Clone(req.Context())

	// Add custom headers, already resolved at launch (resolveLaunchConfig).
	for key, value := range t.headers {
		req.Header.Set(key, value)
	}

	// Use the base transport
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	return base.RoundTrip(req)
}
