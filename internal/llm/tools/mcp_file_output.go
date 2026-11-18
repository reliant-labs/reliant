package tools

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// MCP tools whose output the daemon saves to disk itself (#2820).
//
// chrome-devtools-mcp's take_screenshot takes a filePath and writes the file
// itself, but only inside the MCP client's "roots". Reliant's client declares
// none, so the server falls back to its own working directory — and the
// browser server is shared by name across every chat on the machine, so that
// is whichever project started it. Saves to the chat's own worktree, a sibling
// worktree, or ~/designs were refused with "Access denied: path … is not
// within any of the configured workspace roots", or landed against another
// project's tree.
//
// Implementing MCP roots would not fix it: a shared server answers one roots
// list at a time. So the path never reaches the server. The adapter strips
// it, takes the image the server returns instead, and writes it through the
// tool context's daemon — the same per-chat path policy the write tool and
// save_attachment follow — resolving a relative path against the chat's
// workspace root.

// daemonSavedOutput names the argument a tool takes a destination path in.
type daemonSavedOutput struct {
	pathArg string
	noun    string
}

// daemonSavedMCPOutputs lists, per server and tool, the outputs the daemon
// saves. take_snapshot and performance_stop_trace also take a filePath, but
// return their output as formatted text the server would otherwise have
// written, which cannot be separated from the rest of the response reliably;
// they still go to the server.
var daemonSavedMCPOutputs = map[string]map[string]daemonSavedOutput{
	"chrome-devtools": {
		"take_screenshot": {pathArg: "filePath", noun: "screenshot"},
	},
}

// maxServerSavedImageBytes bounds the read of an image the server saved to a
// temporary file instead of returning it (chrome-devtools-mcp does that for
// screenshots of 2 MB and over).
const maxServerSavedImageBytes = 64 << 20

// serverSavedFileLine matches the line chrome-devtools-mcp writes when it saved
// the image to a file rather than returning it.
var serverSavedFileLine = regexp.MustCompile(`^Saved \w+ to (.+)\.$`)

// daemonSavedOutputFor returns the output spec and requested path when this
// call asks the server to write a file the daemon should write instead, and the
// arguments to send the server — a copy without the path.
func (a *MCPToolAdapter) daemonSavedOutputFor(arguments map[string]interface{}) (daemonSavedOutput, string, map[string]interface{}, bool) {
	spec, ok := daemonSavedMCPOutputs[a.serverName][a.tool.Name]
	if !ok {
		return daemonSavedOutput{}, "", arguments, false
	}
	requested, _ := arguments[spec.pathArg].(string)
	if strings.TrimSpace(requested) == "" {
		return daemonSavedOutput{}, "", arguments, false
	}
	forwarded := make(map[string]interface{}, len(arguments))
	for k, v := range arguments {
		if k != spec.pathArg {
			forwarded[k] = v
		}
	}
	return spec, strings.TrimSpace(requested), forwarded, true
}

// resolveSavePath resolves where the daemon writes a saved output: an absolute
// path as given, a relative one against the chat's workspace root.
func resolveSavePath(tc *rctx.ToolContext, requested string) (string, error) {
	if filepath.IsAbs(requested) {
		return filepath.Clean(requested), nil
	}
	root, err := ResolveRepoPath(tc, "")
	if err != nil {
		return "", fmt.Errorf("couldn't resolve the relative path %q: %w", requested, err)
	}
	return filepath.Join(root, requested), nil
}

// saveMCPImageOutput writes the image in an MCP result to dest through the
// daemon and returns the result the model sees: the server's own text lines
// and where the file was saved, without the image, which is what the tool
// promises when given a path ("instead of attaching it to the response").
//
// Every failure is a tool error naming what happened; the screenshot was taken,
// so the message says so, and the call can be repeated without a path to get
// the image inline.
func (a *MCPToolAdapter) saveMCPImageOutput(tc *rctx.ToolContext, spec daemonSavedOutput, dest string, result *mcp.ToolResult) ToolResponse {
	if result == nil || result.IsError {
		return a.convertResult(result)
	}

	var (
		data      []byte
		mimeType  string
		serverTmp string
		lines     []string
	)
	for _, content := range result.Content {
		switch {
		case content.Type == "image" && data == nil:
			decoded, err := base64.StdEncoding.DecodeString(content.Data)
			if err != nil {
				return NewTextErrorResponse(fmt.Sprintf(
					"Took the %s but could not decode the image the server returned: %v", spec.noun, err))
			}
			data, mimeType = decoded, content.MimeType
		case content.Text != "":
			for _, line := range strings.Split(content.Text, "\n") {
				if m := serverSavedFileLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
					serverTmp = m[1]
					continue
				}
				lines = append(lines, line)
			}
		}
	}

	if data == nil && serverTmp != "" {
		// Too large to return inline: the server saved it to a temporary file
		// on this machine. Read it back through the daemon.
		read, err := tc.Daemon.ReadBinaryFile(tc.Context, serverTmp, maxServerSavedImageBytes)
		if err != nil {
			return NewTextErrorResponse(fmt.Sprintf(
				"Took the %s, which the server saved to %s, but could not read it to save it to %s: %v",
				spec.noun, serverTmp, dest, err))
		}
		data = read
		mimeType = mimeTypeForExtension(filepath.Ext(serverTmp))
	}
	if data == nil {
		return NewTextErrorResponse(fmt.Sprintf(
			"The %s was not saved to %s: the server returned no image.", spec.noun, dest))
	}

	dest = withImageExtension(dest, mimeType)
	if _, err := tc.Daemon.WriteBinaryFile(tc.Context, dest, data); err != nil {
		return NewTextErrorResponse(fmt.Sprintf(
			"Took the %s but could not save it to %s: %v. "+
				"Choose a path this chat may write to, or omit %s to get the image in the response.",
			spec.noun, dest, err, spec.pathArg))
	}
	logging.Debug("Saved MCP tool output through the daemon",
		"server", a.serverName, "tool", a.tool.Name, "path", dest, "bytes", len(data))

	lines = append(lines, fmt.Sprintf("Saved %s to %s.", spec.noun, dest))
	return NewTextResponse(strings.TrimSpace(strings.Join(lines, "\n")))
}

// withImageExtension gives dest the extension of the image's format, replacing
// any other one, as chrome-devtools-mcp does for the files it writes — so a
// JPEG requested as shot.png is saved as shot.jpeg, as it always was.
func withImageExtension(dest, mimeType string) string {
	ext := ""
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		ext = ".png"
	case "image/jpeg", "image/jpg":
		ext = ".jpeg"
	case "image/webp":
		ext = ".webp"
	}
	if ext == "" {
		return dest
	}
	current := filepath.Ext(dest)
	if current == filepath.Base(dest) {
		current = "" // a dotfile's name is not its extension
	}
	return strings.TrimSuffix(dest, current) + ext
}

func mimeTypeForExtension(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	}
	return ""
}
