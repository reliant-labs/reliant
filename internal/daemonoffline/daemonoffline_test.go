// Copyright (c) 2025 Reliant Labs
package daemonoffline

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
)

func TestIsError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "connect CodeUnavailable with exact daemon-offline message",
			err:  connect.NewError(connect.CodeUnavailable, fmt.Errorf("no daemon connected for user")),
			want: true,
		},
		{
			name: "connect CodeUnavailable with unrelated message",
			err:  connect.NewError(connect.CodeUnavailable, fmt.Errorf("backend reconnecting")),
			want: false,
		},
		{
			name: "connect CodeInternal even with daemon substring is rejected",
			err:  connect.NewError(connect.CodeInternal, fmt.Errorf("no daemon connected for user")),
			want: false,
		},
		{
			name: "fmt.Errorf wrapping a connect daemon-offline error",
			err: fmt.Errorf("checking daemon status: %w",
				connect.NewError(connect.CodeUnavailable, fmt.Errorf("no daemon connected for user"))),
			want: true,
		},
		{
			name: "deeply nested fmt.Errorf still detected",
			err: fmt.Errorf("daemon command worktree.validate_path: %w",
				fmt.Errorf("unavailable: no daemon connected for user")),
			want: true,
		},
		{
			name: "plain error with substring (RemoteExecutor flattened path)",
			err:  errors.New("Failed to execute tool on daemon: unavailable: no daemon connected for user"),
			want: true,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection reset by peer"),
			want: false,
		},
		{
			name: "rate limit error (not daemon offline)",
			err:  errors.New("429 Too Many Requests"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsError(tt.err)
			if got != tt.want {
				t.Errorf("IsError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsToolResultContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "empty content",
			content: "",
			want:    false,
		},
		{
			name:    "RemoteExecutor flattened daemon-offline content",
			content: "Failed to execute tool on daemon: unavailable: no daemon connected for user",
			want:    true,
		},
		{
			name:    "Tool execution failed wrapper from execute_tools",
			content: "Tool execution failed: checking daemon status: unavailable: no daemon connected for user",
			want:    true,
		},
		{
			name:    "unrelated tool error",
			content: "permission denied",
			want:    false,
		},
		{
			name:    "successful tool output",
			content: `{"stdout":"hello","stderr":"","exit_code":0}`,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsToolResultContent(tt.content)
			if got != tt.want {
				t.Errorf("IsToolResultContent(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}
