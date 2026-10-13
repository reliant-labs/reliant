package serverclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/reliant/internal/netguard"
)

const protocolVersion = "2025-06-18"

// Entry is a curated, server-owned integration: a fixed URL or a host
// allowlist. It is never built from user-typed config.
type Entry struct {
	Name string
	// URL is the fixed endpoint of the integration.
	URL string
	// AllowedHosts, when non-empty, lists the hosts URL may resolve to.
	// Empty means the host of URL itself is the only allowed host.
	AllowedHosts []string
}

// Creds are the credentials for one call, supplied by the Connections layer.
type Creds struct {
	BearerToken string
	Headers     map[string]string
}

// Tool is a tool advertised by the server.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// client is a streamable-HTTP MCP client bound to one catalog entry.
type client struct {
	entry Entry
	creds Creds
	http  *http.Client

	nextID    atomic.Int64
	mu        sync.Mutex
	sessionID string
	ready     bool
}

// New validates deps.Entry and returns an uninitialised client.
func New(deps Deps) (Service, error) {
	entry := deps.Entry
	u, err := url.Parse(entry.URL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("catalog entry %q: invalid URL %q", entry.Name, entry.URL)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("catalog entry %q: URL must be https, got %q", entry.Name, u.Scheme)
	}
	if !hostAllowed(entry, u.Hostname()) {
		return nil, fmt.Errorf("catalog entry %q: host %q is not allowed", entry.Name, u.Hostname())
	}
	var guard DialGuard = netguard.New()
	if deps.Guard != nil {
		guard = deps.Guard
	}
	return &client{entry: entry, creds: deps.Creds, http: &http.Client{Timeout: 60 * time.Second, Transport: guard.Transport()}}, nil
}

func hostAllowed(entry Entry, host string) bool {
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	if len(entry.AllowedHosts) == 0 {
		u, _ := url.Parse(entry.URL)
		return strings.EqualFold(u.Hostname(), host)
	}
	for _, allowed := range entry.AllowedHosts {
		if strings.EqualFold(allowed, host) {
			return true
		}
	}
	return false
}

// Initialize implements Service.
func (c *client) Initialize(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ready {
		return nil
	}
	if _, err := c.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "reliant", "version": "1.0.0"},
	}); err != nil {
		return fmt.Errorf("initialize %s: %w", c.entry.Name, err)
	}
	if err := c.notify(ctx, "notifications/initialized"); err != nil {
		return fmt.Errorf("initialized %s: %w", c.entry.Name, err)
	}
	c.ready = true
	return nil
}

// ListTools implements Service.
func (c *client) ListTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	out := make([]Tool, 0, len(resp.Tools))
	for _, t := range resp.Tools {
		out = append(out, Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out, nil
}

// CallTool implements Service.
func (c *client) CallTool(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	return c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
}

// Close implements Service.
func (c *client) Close() error {
	c.mu.Lock()
	sid := c.sessionID
	c.sessionID, c.ready = "", false
	c.mu.Unlock()
	if sid == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, c.entry.URL, nil)
	if err != nil {
		return err
	}
	c.decorate(req, sid)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := c.Initialize(ctx); err != nil {
		return nil, err
	}
	return c.rpc(ctx, method, params)
}

func (c *client) decorate(req *http.Request, sessionID string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
		req.Header.Set("Mcp-Protocol-Version", protocolVersion)
	}
	for k, v := range c.creds.Headers {
		req.Header.Set(k, v)
	}
	if c.creds.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.creds.BearerToken)
	}
}

func (c *client) notify(ctx context.Context, method string) error {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	resp, err := c.post(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return nil
}

func (c *client) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.entry.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.decorate(req, c.sessionID)
	return c.http.Do(req)
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *client) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}

	var out rpcResponse
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		out, err = readSSEResponse(resp.Body, id)
	} else {
		err = json.NewDecoder(resp.Body).Decode(&out)
	}
	if err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("mcp error %d: %s", out.Error.Code, out.Error.Message)
	}
	return out.Result, nil
}

func readSSEResponse(r io.Reader, wantID int64) (rpcResponse, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	want := fmt.Sprint(wantID)
	for scanner.Scan() {
		line := scanner.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		var msg rpcResponse
		if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &msg); err != nil {
			continue
		}
		if string(msg.ID) == want {
			return msg, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return rpcResponse{}, err
	}
	return rpcResponse{}, errors.New("event stream ended without a response")
}
