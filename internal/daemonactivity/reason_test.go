// Copyright (c) 2025 Reliant Labs

package daemonactivity

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func command(commandType string) *reliantv1.ServerMessage {
	return &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_DaemonCommand{
		DaemonCommand: &reliantv1.DaemonCommandRequest{RequestId: "r", CommandType: commandType},
	}}
}

func terminalInput(data string) *reliantv1.ServerMessage {
	return &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_TerminalInput{
		TerminalInput: &reliantv1.TerminalInputMessage{SessionId: "s", Data: []byte(data)},
	}}
}

// The traffic that kept a machine awake overnight on 2026-10-11 must not be
// activity: the reconciler's exec.bg_status poll, an open tab's terminal
// reconnects and file-tree refreshes, MCP status checks, and the gateway's
// own keepalive.
func TestBackgroundTrafficIsNotActivity(t *testing.T) {
	cases := map[string]*reliantv1.ServerMessage{
		"reconciler bg_status poll": command("exec.bg_status"),
		"file tree refresh":         command("fs.get_tree"),
		"terminal reconnect":        command("terminal.create"),
		"mcp status poll":           command("mcp.server_status"),
		"worktree sweeper":          command("worktree.reconcile"),
		"agent step worktree check": command("worktree.ensure"),
		"unclassified command":      command("some.future_command"),
		"gateway heartbeat": {Message: &reliantv1.ServerMessage_Heartbeat{
			Heartbeat: &reliantv1.ServerHeartbeat{Timestamp: 1},
		}},
		"terminal resize": {Message: &reliantv1.ServerMessage_TerminalResize{
			TerminalResize: &reliantv1.TerminalResizeMessage{SessionId: "s", Cols: 80, Rows: 24},
		}},
		"terminal output subscribe": {Message: &reliantv1.ServerMessage_TerminalOutputSubscribe{
			TerminalOutputSubscribe: &reliantv1.TerminalOutputSubscribeMessage{SessionId: "s"},
		}},
		"config watch": {Message: &reliantv1.ServerMessage_WatchProjectConfigs{
			WatchProjectConfigs: &reliantv1.WatchProjectConfigsRequest{ProjectPath: "/p"},
		}},
		"terminal answering a device-attributes query": terminalInput("\x1b[?1;2c"),
		"empty message": {},
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			if reason, ok := ReasonForServerMessage(msg); ok {
				t.Errorf("counted as activity (%q); it must not keep the machine awake", reason)
			}
		})
	}
}

// Real work from a person or an agent must still keep the machine awake, and
// say why.
func TestUserAndAgentWorkIsActivity(t *testing.T) {
	cases := []struct {
		name string
		msg  *reliantv1.ServerMessage
		want string
	}{
		{
			name: "agent tool call",
			msg: &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_ToolRequest{
				ToolRequest: &reliantv1.ToolRequest{RequestId: "r", ToolName: "shell"},
			}},
			want: "tool call shell",
		},
		{name: "typing in a terminal", msg: terminalInput("ls -la\r"), want: "terminal input"},
		{name: "arrow key", msg: terminalInput("\x1b[A"), want: "terminal input"},
		{name: "saving a file", msg: command("fs.write_file"), want: "command fs.write_file"},
		{name: "committing", msg: command("worktree.commit"), want: "command worktree.commit"},
		{name: "agent MCP tool", msg: command("mcp.call_tool"), want: "command mcp.call_tool"},
		{
			name: "killing a process",
			msg: &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_KillProcess{
				KillProcess: &reliantv1.DaemonKillProcessRequest{ProcessId: "p"},
			}},
			want: "process kill",
		},
		{
			name: "local model call",
			msg: &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_LocalModelHttpRequest{
				LocalModelHttpRequest: &reliantv1.LocalModelHTTPRequest{RequestId: "r"},
			}},
			want: "local model request",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := ReasonForServerMessage(tc.msg)
			if !ok {
				t.Fatalf("not counted as activity; it must keep the machine awake")
			}
			if reason != tc.want {
				t.Errorf("reason = %q, want %q", reason, tc.want)
			}
		})
	}
}

func TestIsTerminalReport(t *testing.T) {
	reports := map[string]string{
		"DA1 reply":                "\x1b[?1;2c",
		"DA2 reply":                "\x1b[>0;276;0c",
		"cursor position report":   "\x1b[24;80R",
		"device status":            "\x1b[0n",
		"focus in":                 "\x1b[I",
		"focus out":                "\x1b[O",
		"OSC colour reply, BEL":    "\x1b]11;rgb:1e1e/1e1e/1e1e\x07",
		"OSC colour reply, ST":     "\x1b]10;rgb:ffff/ffff/ffff\x1b\\",
		"two replies back to back": "\x1b[?1;2c\x1b[24;80R",
		"DECRPM reply":             "\x1b[?2004;2$y",
	}
	for name, data := range reports {
		if !IsTerminalReport([]byte(data)) {
			t.Errorf("%s (%q): not recognised as a terminal report", name, data)
		}
	}

	input := map[string]string{
		"empty":                   "",
		"a letter":                "a",
		"enter":                   "\r",
		"ctrl-c":                  "\x03",
		"arrow up":                "\x1b[A",
		"bare escape":             "\x1b",
		"report then a keystroke": "\x1b[?1;2cls",
		"unterminated OSC":        "\x1b]11;rgb:1e1e",
		"pasted text":             "echo hello\r",
		"page up (CSI 5 ~)":       "\x1b[5~",
	}
	for name, data := range input {
		if IsTerminalReport([]byte(data)) {
			t.Errorf("%s (%q): treated as a terminal report, but a person typed it", name, data)
		}
	}
}

func TestCommandTablesAreDisjoint(t *testing.T) {
	for commandType := range userActionCommands {
		if backgroundCommands[commandType] {
			t.Errorf("%s is listed as both activity and background", commandType)
		}
	}
}
