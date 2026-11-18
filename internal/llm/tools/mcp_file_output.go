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
// workspace root. The destination must be a new file: an existing one is
// never replaced (writeSavedOutputLocked says why).

// daemonSavedOutput names the argument a tool takes a destination path in.
type daemonSavedOutput struct {
	pathArg string
	noun    string
}

// daemonSavedOutputNote is appended to the description of a tool whose output
// the daemon saves, because the server's own description of the path argument
// no longer holds.
func daemonSavedOutputNote(spec daemonSavedOutput) string {
	return fmt.Sprintf("\n\nReliant saves the %s to %s itself: a relative path resolves against the chat's workspace, "+
		"and the path must be a new file — an existing file is never replaced.", spec.noun, spec.pathArg)
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

// serverSavedFileLine matches the line chrome-devtools-mcp writes when it saved
// the image to a temporary file of its own rather than returning it (it does
// that for screenshots of 2 MB and over). The path in it is never read: it is
// text from the server, and reading it would let a response copy any file the
// daemon can read to the destination. Only the image the result carries is
// written.
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
		data        []byte
		mimeType    string
		serverSaved bool
		lines       []string
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
				if serverSavedFileLine.MatchString(strings.TrimSpace(line)) {
					serverSaved = true
					continue
				}
				lines = append(lines, line)
			}
		}
	}

	if data == nil && serverSaved {
		return NewTextErrorResponse(fmt.Sprintf(
			"The %s was not saved to %s: it was too large for the server to return (2 MB or more), "+
				"so the server kept it in a temporary file of its own, which Reliant does not read. "+
				"Retry with format \"jpeg\" or \"webp\" and a lower quality, or without fullPage.",
			spec.noun, dest))
	}
	if data == nil {
		return NewTextErrorResponse(fmt.Sprintf(
			"The %s was not saved to %s: the server returned no image.", spec.noun, dest))
	}

	dest = withImageExtension(dest, mimeType)
	resp, err := withPathLock(tc, dest, func() (ToolResponse, error) {
		return a.writeSavedOutputLocked(tc, spec, dest, data), nil
	})
	if err != nil || resp.IsError {
		return resp
	}

	lines = append(lines, fmt.Sprintf("Saved %s to %s.", spec.noun, dest))
	return NewTextResponse(strings.TrimSpace(strings.Join(lines, "\n")))
}

// maxFreePathProbes bounds the search for a free name to suggest when the
// destination exists.
const maxFreePathProbes = 20

// writeSavedOutputLocked writes data to dest, which must not exist yet, and
// returns an error response when it refuses or fails (a zero response on
// success). Callers must hold the path lock for dest.
//
// An existing file is never replaced, even one this thread read. The daemon's
// file API has no conditional write — no compare-and-write against an observed
// version, no create-exclusive — so a check followed by a write could not stop
// another process changing the file in between, and the previous bytes of a
// binary file are not returned to put back. Refusing every existing
// destination leaves only the window where the file is created between the
// stat and the write; WriteResult.Created reports that case, and it is a tool
// error rather than a claimed clean save.
func (a *MCPToolAdapter) writeSavedOutputLocked(tc *rctx.ToolContext, spec daemonSavedOutput, dest string, data []byte) ToolResponse {
	chatID, thread := tc.ChatID, tc.Thread
	if thread == "" {
		thread = "0"
	}
	notSaved := func(reason string) ToolResponse {
		return NewTextErrorResponse(fmt.Sprintf(
			"Took the %s but did not save it to %s: %s", spec.noun, dest, reason))
	}

	stat, err := tc.Daemon.StatFile(tc.Context, dest)
	if err != nil {
		return notSaved(fmt.Sprintf("could not check the path: %v.", err))
	}
	if stat.Exists {
		reason := "a file already exists there, and a saved " + spec.noun + " never replaces one, so it was left untouched."
		if stat.IsDir {
			reason = "the path is a directory."
		}
		if free := freeSiblingPath(tc, dest); free != "" {
			reason += fmt.Sprintf(" Save to a new path instead, such as %s.", free)
		} else {
			reason += " Save to a new path instead."
		}
		return notSaved(reason)
	}

	res, err := tc.Daemon.WriteBinaryFile(tc.Context, dest, data)
	if err != nil {
		return notSaved(fmt.Sprintf(
			"%v. Choose a path this chat may write to, or omit %s to get the image in the response.",
			err, spec.pathArg))
	}
	recordFileAwareness(chatID, thread, dest)
	if res != nil && !res.Created {
		return NewTextErrorResponse(fmt.Sprintf(
			"Took the %s and wrote it to %s, but a file appeared there while it was being saved and was replaced; "+
				"its previous contents could not be kept. Check that file, and save to a new path next time.",
			spec.noun, dest))
	}
	logging.Debug("Saved MCP tool output through the daemon",
		"server", a.serverName, "tool", a.tool.Name, "path", dest, "bytes", len(data))
	return ToolResponse{}
}

// freeSiblingPath returns the first of name-1.ext, name-2.ext, … beside dest
// that does not exist, or "" when none of the first few is free or the daemon
// cannot say. It is a suggestion for the model, not a reservation.
func freeSiblingPath(tc *rctx.ToolContext, dest string) string {
	ext := filepath.Ext(dest)
	if ext == filepath.Base(dest) {
		ext = ""
	}
	stem := strings.TrimSuffix(dest, ext)
	for i := 1; i <= maxFreePathProbes; i++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, i, ext)
		stat, err := tc.Daemon.StatFile(tc.Context, candidate)
		if err != nil {
			return ""
		}
		if !stat.Exists {
			return candidate
		}
	}
	return ""
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
