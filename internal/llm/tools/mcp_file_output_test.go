package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// #2820: chrome-devtools refused to save screenshots anywhere outside the
// shared server's working directory — another project's tree. The daemon now
// writes the file, under the chat's path rules.

var screenshotBytes = []byte("\x89PNG\r\n\x1a\nnot-really-a-png\xff\x00")

// screenshotRuntime is a fake MCP runtime that records the arguments a call
// reached the server with and answers with a fixed result.
type screenshotRuntime struct {
	sessionRecordingRuntime
	args   map[string]interface{}
	result *mcp.ToolResult
}

func (r *screenshotRuntime) ProjectCallTool(_, _, _, _ string, args map[string]interface{}) (*mcp.ToolResult, error) {
	r.args = args
	return r.result, nil
}

func (r *screenshotRuntime) CallTool(_, _, _ string, args map[string]interface{}) (*mcp.ToolResult, error) {
	r.args = args
	return r.result, nil
}

func inlineScreenshot(mime string) *mcp.ToolResult {
	return &mcp.ToolResult{Content: []mcp.ToolContent{
		{Type: "text", Text: "# take_screenshot response\nTook a screenshot of the current page's viewport."},
		{Type: "image", MimeType: mime, Data: base64.StdEncoding.EncodeToString(screenshotBytes)},
	}}
}

// fileDaemon records binary writes, serves binary reads from files, and
// refuses writes under a denied prefix the way the daemon's path policy does.
type fileDaemon struct {
	daemon.Client
	files      map[string][]byte
	denyPrefix string
	writes     []string
}

func (d *fileDaemon) WriteBinaryFile(_ context.Context, path string, content []byte) (*daemon.WriteResult, error) {
	if d.denyPrefix != "" && strings.HasPrefix(path, d.denyPrefix) {
		return nil, errors.New("path is outside the directories this run may use")
	}
	d.writes = append(d.writes, path)
	if d.files == nil {
		d.files = map[string][]byte{}
	}
	d.files[path] = append([]byte(nil), content...)
	return &daemon.WriteResult{Created: true, BytesWritten: len(content)}, nil
}

func (d *fileDaemon) ReadBinaryFile(_ context.Context, path string, _ int64) ([]byte, error) {
	data, ok := d.files[path]
	if !ok {
		return nil, errors.New("no such file")
	}
	return data, nil
}

func runScreenshot(t *testing.T, server string, runtime *screenshotRuntime, d *fileDaemon, input map[string]interface{}) ToolResponse {
	t.Helper()
	adapter, err := NewProjectMCPToolAdapter("/workspace", server, mcp.Tool{Name: "take_screenshot"})
	if err != nil {
		t.Fatalf("NewProjectMCPToolAdapter: %v", err)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var tc *rctx.ToolContext = createTestContext(t, "chat-1")
	tc.Project = &rctx.Project{Checkout: "/home/workspace/worktrees/chat-1"}
	tc = tc.WithMCP(runtime)
	if d != nil {
		tc = tc.WithDaemon(d)
	}
	resp, err := adapter.Run(tc, ToolCall{ID: "1", Name: adapter.Name(), Input: string(raw)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return resp
}

func TestMCPScreenshot_RelativePathIsSavedInTheChatsWorktree(t *testing.T) {
	runtime := &screenshotRuntime{result: inlineScreenshot("image/png")}
	d := &fileDaemon{}
	resp := runScreenshot(t, "chrome-devtools", runtime, d, map[string]interface{}{
		"filePath": "shots/home.png", "format": "png",
	})

	if resp.IsError {
		t.Fatalf("expected success, got error: %s", resp.Content)
	}
	if _, sent := runtime.args["filePath"]; sent {
		t.Fatal("filePath must not reach the shared server: its idea of where it may write is not this chat's")
	}
	if runtime.args["format"] != "png" {
		t.Fatalf("the other arguments must reach the server unchanged, got %v", runtime.args)
	}
	const want = "/home/workspace/worktrees/chat-1/shots/home.png"
	if !bytes.Equal(d.files[want], screenshotBytes) {
		t.Fatalf("expected the image bytes written to %s, wrote %v", want, d.writes)
	}
	if !strings.Contains(resp.Content, "Saved screenshot to "+want+".") {
		t.Fatalf("result must say where the file went, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "Took a screenshot") {
		t.Fatalf("the server's own text must be kept, got %q", resp.Content)
	}
	if len(resp.BinaryParts) != 0 {
		t.Fatal("given a path, the screenshot is saved instead of attached, as the tool documents")
	}
}

func TestMCPScreenshot_AbsolutePathOutsideTheServersTreeIsSaved(t *testing.T) {
	d := &fileDaemon{}
	resp := runScreenshot(t, "chrome-devtools", &screenshotRuntime{result: inlineScreenshot("image/png")}, d,
		map[string]interface{}{"filePath": "/home/workspace/designs/landing.png"})
	if resp.IsError {
		t.Fatalf("expected success, got error: %s", resp.Content)
	}
	if _, ok := d.files["/home/workspace/designs/landing.png"]; !ok {
		t.Fatalf("expected a write to the requested absolute path, wrote %v", d.writes)
	}
}

func TestMCPScreenshot_PathThePolicyRefusesIsAClearToolError(t *testing.T) {
	d := &fileDaemon{denyPrefix: "/etc/"}
	resp := runScreenshot(t, "chrome-devtools", &screenshotRuntime{result: inlineScreenshot("image/png")}, d,
		map[string]interface{}{"filePath": "/etc/shot.png"})
	if !resp.IsError {
		t.Fatalf("a refused write must be a tool error, got %q", resp.Content)
	}
	for _, want := range []string{"/etc/shot.png", "outside the directories this run may use", "omit filePath"} {
		if !strings.Contains(resp.Content, want) {
			t.Fatalf("error should mention %q, got %q", want, resp.Content)
		}
	}
	if len(d.writes) != 0 {
		t.Fatalf("nothing may be written, wrote %v", d.writes)
	}
}

func TestMCPScreenshot_LargeScreenshotTheServerSavedToTmpIsCopied(t *testing.T) {
	const tmp = "/tmp/chrome-devtools-mcp-abc/screenshot.png"
	d := &fileDaemon{files: map[string][]byte{tmp: screenshotBytes}}
	runtime := &screenshotRuntime{result: &mcp.ToolResult{Content: []mcp.ToolContent{
		{Type: "text", Text: "Took a screenshot of the full current page.\nSaved screenshot to " + tmp + "."},
	}}}
	resp := runScreenshot(t, "chrome-devtools", runtime, d, map[string]interface{}{"filePath": "full.png", "fullPage": true})
	if resp.IsError {
		t.Fatalf("expected success, got error: %s", resp.Content)
	}
	const want = "/home/workspace/worktrees/chat-1/full.png"
	if !bytes.Equal(d.files[want], screenshotBytes) {
		t.Fatalf("expected the server's temp file copied to %s, wrote %v", want, d.writes)
	}
	if strings.Contains(resp.Content, tmp) {
		t.Fatalf("the temp path is an implementation detail, got %q", resp.Content)
	}
}

func TestMCPScreenshot_ExtensionFollowsTheImageFormat(t *testing.T) {
	d := &fileDaemon{}
	resp := runScreenshot(t, "chrome-devtools", &screenshotRuntime{result: inlineScreenshot("image/jpeg")}, d,
		map[string]interface{}{"filePath": "/w/shot.png", "format": "jpeg"})
	if resp.IsError {
		t.Fatalf("expected success, got error: %s", resp.Content)
	}
	if _, ok := d.files["/w/shot.jpeg"]; !ok {
		t.Fatalf("a JPEG is saved with its own extension, as chrome-devtools-mcp did; wrote %v", d.writes)
	}
}

func TestMCPScreenshot_WithoutAPathTheImageIsReturnedAsBefore(t *testing.T) {
	d := &fileDaemon{}
	runtime := &screenshotRuntime{result: inlineScreenshot("image/png")}
	resp := runScreenshot(t, "chrome-devtools", runtime, d, map[string]interface{}{"format": "png"})
	if resp.IsError || len(resp.BinaryParts) != 1 {
		t.Fatalf("expected the image attached, got error=%v parts=%d", resp.IsError, len(resp.BinaryParts))
	}
	if len(d.writes) != 0 {
		t.Fatalf("nothing is written without a path, wrote %v", d.writes)
	}
}

func TestMCPScreenshot_OtherServersKeepTheirFilePath(t *testing.T) {
	runtime := &screenshotRuntime{result: inlineScreenshot("image/png")}
	resp := runScreenshot(t, "my-browser", runtime, &fileDaemon{}, map[string]interface{}{"filePath": "x.png"})
	if resp.IsError {
		t.Fatalf("unexpected error: %s", resp.Content)
	}
	if runtime.args["filePath"] != "x.png" {
		t.Fatalf("only the built-in chrome-devtools server is rewritten, got %v", runtime.args)
	}
}

func TestMCPScreenshot_NoDaemonIsAClearToolErrorBeforeTheCall(t *testing.T) {
	runtime := &screenshotRuntime{result: inlineScreenshot("image/png")}
	resp := runScreenshot(t, "chrome-devtools", runtime, nil, map[string]interface{}{"filePath": "x.png"})
	if !resp.IsError || !strings.Contains(resp.Content, "requires a connected daemon") {
		t.Fatalf("expected a daemon-required error, got %q", resp.Content)
	}
	if runtime.args != nil {
		t.Fatal("the screenshot must not be taken when it cannot be saved")
	}
}
